// Package discovery implements the three sources portico merges into a
// single service list: local Docker containers, Tailscale tailnet peers,
// and raw HTTP port probing.
package discovery

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"
)

// Label prefix used to opt containers in/out and override display metadata.
// e.g. portico.enable=false, portico.name=Jellyfin, portico.icon=<url>, portico.category=Media
const labelPrefix = "portico."

// DockerContainer is the subset of a discovered container's info we care about.
type DockerContainer struct {
	Name   string
	Labels map[string]string
	Ports  []int // host-published ports (0.0.0.0 or any host binding)
}

// DockerClient talks to the Docker Engine API over a unix socket.
type DockerClient struct {
	httpClient *http.Client
}

func NewDockerClient(socketPath string) *DockerClient {
	return &DockerClient{
		httpClient: &http.Client{
			Timeout: 5 * time.Second,
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					d := net.Dialer{}
					return d.DialContext(ctx, "unix", socketPath)
				},
			},
		},
	}
}

// Available reports whether the Docker socket is reachable at all, so callers
// can silently skip this source when it's not mounted.
func (c *DockerClient) Available(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/_ping", nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	return resp.StatusCode == http.StatusOK
}

type rawContainer struct {
	Names  []string          `json:"Names"`
	Labels map[string]string `json:"Labels"`
	Ports  []struct {
		PublicPort uint16 `json:"PublicPort"`
		Type       string `json:"Type"`
	} `json:"Ports"`
}

// List returns the running containers that opted in to discovery (i.e. did
// not set portico.enable=false), along with their published host ports.
func (c *DockerClient) List(ctx context.Context) ([]DockerContainer, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/containers/json?all=false", nil)
	if err != nil {
		return nil, err
	}
	resp, err := c.httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("docker api: %s: %s", resp.Status, string(body))
	}

	var raw []rawContainer
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		return nil, err
	}

	out := make([]DockerContainer, 0, len(raw))
	for _, rc := range raw {
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

// Label helpers.

func (d DockerContainer) label(key string) string {
	return d.Labels[labelPrefix+key]
}

func (d DockerContainer) NameOverride() string { return d.label("name") }
func (d DockerContainer) IconOverride() string { return d.label("icon") }
func (d DockerContainer) Category() string     { return d.label("category") }

// ExplicitURL lets a container declare its own reachable URL directly
// (portico.url=https://host:port) instead of relying on published-port probing.
func (d DockerContainer) ExplicitURL() string { return d.label("url") }
