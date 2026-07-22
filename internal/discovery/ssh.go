package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	"golang.org/x/crypto/ssh"
)

// SSHPUser is the SSH username for tailnet connections.
// Tailscale SSH authenticates via node key, not this value — it just satisfies
// the SSH protocol's user field.
const SSHPUser = "root"

// SSHProbe discovers services on remote tailnet peers by SSHing in and
// inspecting listening ports (ss -tlnp) and Docker containers (docker ps).
// It composes DockerProbe.ParseRemote to avoid duplicating docker ps parsing.
type SSHProbe struct {
	docker      *DockerProbe
	timeout     time.Duration
	concurrency int
	log         *slog.Logger
}

func NewSSHProbe(docker *DockerProbe, timeout time.Duration, concurrency int, log *slog.Logger) *SSHProbe {
	return &SSHProbe{
		docker:      docker,
		timeout:     timeout,
		concurrency: concurrency,
		log:         log,
	}
}

// ProbeHost dials a host via SSH and returns discovered targets (ports
// and Docker containers). The Orchestrator probes each target via HTTP
// after discovery, same as sweepPass targets.
func (p *SSHProbe) ProbeHost(ctx context.Context, host TailnetHost) ([]target, error) {
	if len(host.IPs) == 0 {
		return nil, fmt.Errorf("no tailscale IP for host %q", host.Hostname)
	}

	addr := net.JoinHostPort(host.IPs[0].String(), "22")

	config := &ssh.ClientConfig{
		User: SSHPUser,
		Auth: []ssh.AuthMethod{
			ssh.PasswordCallback(func() (string, error) { return "", nil }),
		},
		HostKeyCallback: ssh.InsecureIgnoreHostKey(),
		Timeout:         p.timeout,
	}

	dialCtx, cancel := context.WithTimeout(ctx, p.timeout)
	defer cancel()

	var d net.Dialer
	conn, err := d.DialContext(dialCtx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", addr, err)
	}

	sshConn, chans, reqs, err := ssh.NewClientConn(conn, addr, config)
	if err != nil {
		conn.Close()
		return nil, fmt.Errorf("ssh handshake %s: %w", addr, err)
	}
	client := ssh.NewClient(sshConn, chans, reqs)
	defer client.Close()

	hostname := host.Hostname
	if hostname == "" {
		hostname = host.IPs[0].String()
	}
	fqdn := host.FQDN

	var results []target

	// ss -tlnp
	ssPorts := p.runSS(ctx, client)
	portToTarget := make(map[int]target, len(ssPorts))
	for _, port := range ssPorts {
		portToTarget[port] = target{
			host: hostname,
			fqdn: fqdn,
			addr: host.IPs[0].String(),
			port: port,
		}
	}

	// docker ps — merge: prefer Docker metadata when port appears in both.
	containers := p.runDockerPS(ctx, client)
	for _, c := range containers {
		dc := c // copy for pointer stability
		for _, port := range dc.Ports {
			portToTarget[port] = target{
				host:   hostname,
				fqdn:   fqdn,
				addr:   host.IPs[0].String(),
				port:   port,
				docker: &dc,
			}
		}
	}

	for _, t := range portToTarget {
		results = append(results, t)
	}

	return results, nil
}

func (p *SSHProbe) runSS(ctx context.Context, client *ssh.Client) []int {
	sess, err := client.NewSession()
	if err != nil {
		p.log.Debug("ssh: new session failed for ss", "err", err)
		return nil
	}
	defer sess.Close()

	output, err := sess.Output("ss -tlnp")
	if err != nil {
		p.log.Warn("ssh: ss -tlnp failed", "err", err)
		return nil
	}

	ports, err := parseSSOutput(output)
	if err != nil {
		p.log.Warn("ssh: ss output parse failed", "err", err)
		return nil
	}
	return ports
}

func (p *SSHProbe) runDockerPS(ctx context.Context, client *ssh.Client) []DockerContainer {
	sess, err := client.NewSession()
	if err != nil {
		p.log.Debug("ssh: new session failed for docker ps", "err", err)
		return nil
	}
	defer sess.Close()

	output, err := sess.Output("docker ps --format '{{json .}}'")
	if err != nil {
		if strings.Contains(err.Error(), "permission denied") {
			p.log.Info("ssh: docker ps permission denied — add user to docker group on remote host", "err", err)
		} else {
			p.log.Warn("ssh: docker ps failed", "err", err)
		}
		return nil
	}

	containers, err := p.docker.ParseRemote(output)
	if err != nil {
		p.log.Warn("ssh: docker ps parse failed", "err", err)
		return nil
	}
	return containers
}

// parseSSOutput extracts listening TCP port numbers from ss -tlnp output.
// Skips loopback addresses (127.0.0.1, ::1), port 22, and IPv6 duplicates
// of IPv4 listeners.
func parseSSOutput(output []byte) ([]int, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	ports := make([]int, 0, len(lines))
	seen := make(map[int]bool)

	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" || !strings.Contains(line, "LISTEN") {
			continue
		}

		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}

		// The Local Address:Port column is the 4th field (0-indexed: 3).
		// Example line: LISTEN 0 4096 0.0.0.0:8080 0.0.0.0:* ...
		localAddr := fields[3]

		// Skip loopback addresses.
		if strings.HasPrefix(localAddr, "127.0.0.1:") || strings.HasPrefix(localAddr, "::1") || strings.HasPrefix(localAddr, "[::1]:") {
			continue
		}

		// Extract port from last colon-separated segment.
		// Formats: "0.0.0.0:8080", "[::]:3000", "*:9090"
		idx := strings.LastIndex(localAddr, ":")
		if idx < 0 {
			continue
		}
		portStr := localAddr[idx+1:]
		// Strip trailing bracket from IPv6-style "[::]:3000" -> "3000]"
		portStr = strings.TrimRight(portStr, "]")

		port, err := strconv.Atoi(portStr)
		if err != nil {
			continue
		}

		if port == 22 || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}

	return ports, nil
}
