package discovery

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
)

// DockerProbe discovers containers from the local Docker socket and parses
// remote docker ps output (used by SSHProbe to avoid duplicating parsing).
type DockerProbe struct {
	client *DockerClient
}

func NewDockerProbe(client *DockerClient) *DockerProbe {
	return &DockerProbe{client: client}
}

// Local lists containers on this machine via the Docker socket.
func (p *DockerProbe) Local(ctx context.Context) ([]DockerContainer, error) {
	return p.client.List(ctx)
}

// parseRemoteContainers parses one JSON object per line, in the shape the
// docker CLI (not the Engine API) emits for `--format '{{json .}}'`: Names,
// Labels, and Ports are all pre-formatted strings, e.g.
// Names="nginx", Labels="portico.name=Web,portico.enable=false",
// Ports="0.0.0.0:8080->80/tcp, :::8080->80/tcp". This differs from the
// Engine API's /containers/json shape (structured Names []string, Labels
// map[string]string, Ports []struct) that DockerClient.List parses locally.
func parseRemoteContainers(output []byte) ([]DockerContainer, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	out := make([]DockerContainer, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rc struct {
			Names  string `json:"Names"`
			Labels string `json:"Labels"`
			Ports  string `json:"Ports"`
		}
		if err := json.Unmarshal([]byte(line), &rc); err != nil {
			continue
		}
		labels := parseDockerLabelsField(rc.Labels)
		if strings.EqualFold(labels[labelPrefix+"enable"], "false") {
			continue
		}
		name := strings.TrimSpace(strings.SplitN(rc.Names, ",", 2)[0])
		out = append(out, DockerContainer{Name: name, Labels: labels, Ports: parseDockerPortsField(rc.Ports)})
	}
	return out, nil
}

// parseDockerLabelsField parses the CLI's comma-separated "k=v,k=v" labels
// column into a map.
func parseDockerLabelsField(s string) map[string]string {
	labels := make(map[string]string)
	if s == "" {
		return labels
	}
	for _, kv := range strings.Split(s, ",") {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		labels[strings.TrimSpace(k)] = v
	}
	return labels
}

// parseDockerPortsField extracts host-published TCP ports from the CLI's
// "Ports" column, e.g. "0.0.0.0:8080->80/tcp, :::8080->80/tcp". Entries with
// no "->" are exposed-but-unpublished and unreachable; entries bound only to
// loopback (127.0.0.1, ::1) aren't reachable over the tailnet either, mirroring
// parseSSOutput's loopback skip.
func parseDockerPortsField(s string) []int {
	if s == "" {
		return nil
	}
	var ports []int
	seen := make(map[int]bool)
	for _, seg := range strings.Split(s, ",") {
		seg = strings.TrimSpace(seg)
		hostPart, containerPart, ok := strings.Cut(seg, "->")
		if !ok {
			continue
		}
		if !strings.HasSuffix(containerPart, "/tcp") {
			continue
		}
		idx := strings.LastIndex(hostPart, ":")
		if idx < 0 {
			continue
		}
		hostIP := strings.Trim(hostPart[:idx], "[]")
		if hostIP == "127.0.0.1" || hostIP == "::1" {
			continue
		}
		port, err := strconv.Atoi(hostPart[idx+1:])
		if err != nil || port == 0 || seen[port] {
			continue
		}
		seen[port] = true
		ports = append(ports, port)
	}
	return ports
}

// ParseRemote parses the raw stdout of: docker ps --format '{{json .}}'
// from a remote host.
func (p *DockerProbe) ParseRemote(output []byte) ([]DockerContainer, error) {
	return parseRemoteContainers(output)
}
