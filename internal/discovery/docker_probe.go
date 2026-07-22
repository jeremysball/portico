package discovery

import (
	"context"
	"encoding/json"
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

func parseRemoteContainers(output []byte) ([]DockerContainer, error) {
	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	out := make([]DockerContainer, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		var rc struct {
			Names  []string          `json:"Names"`
			Labels map[string]string `json:"Labels"`
			Ports  []struct {
				PublicPort uint16 `json:"PublicPort"`
				Type       string `json:"Type"`
			} `json:"Ports"`
		}
		if err := json.Unmarshal([]byte(line), &rc); err != nil {
			continue
		}
		if strings.EqualFold(rc.Labels[labelPrefix+"enable"], "false") {
			continue
		}
		name := ""
		if len(rc.Names) > 0 {
			name = strings.TrimPrefix(rc.Names[0], "/")
		}
		ports := make([]int, 0, len(rc.Ports))
		seen := make(map[int]bool)
		for _, p := range rc.Ports {
			if p.Type != "tcp" || p.PublicPort == 0 {
				continue
			}
			port := int(p.PublicPort)
			if !seen[port] {
				seen[port] = true
				ports = append(ports, port)
			}
		}
		out = append(out, DockerContainer{Name: name, Labels: rc.Labels, Ports: ports})
	}
	return out, nil
}

// ParseRemote parses the raw stdout of: docker ps --format '{{json .}}'
// from a remote host. The JSON structure matches the Docker Engine API
// container list response, same as what DockerClient.List() parses.
func (p *DockerProbe) ParseRemote(output []byte) ([]DockerContainer, error) {
	return parseRemoteContainers(output)
}
