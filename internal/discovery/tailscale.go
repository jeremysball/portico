package discovery

import (
	"context"
	"net/netip"
	"strings"

	"tailscale.com/client/local"
)

// TailnetHost is one online node on the tailnet (including this machine).
type TailnetHost struct {
	Hostname string // short display name, used for grouping/category (Tailscale HostInfo hostname, not a DNS name)
	FQDN     string // full tailnet DNS name, e.g. "host.tailnet.ts.net" — used for URLs and TLS SNI so links and probes match what a browser sees; may be empty
	IPs      []netip.Addr
	IsSelf   bool
}

// TailscaleClient lists online peers via the local tailscaled socket.
type TailscaleClient struct {
	lc *local.Client
}

func NewTailscaleClient(socketPath string) *TailscaleClient {
	lc := &local.Client{Socket: socketPath, UseSocketOnly: socketPath != ""}
	return &TailscaleClient{lc: lc}
}

// Available reports whether tailscaled is reachable at all.
func (c *TailscaleClient) Available(ctx context.Context) bool {
	_, err := c.lc.Status(ctx)
	return err == nil
}

// Hosts returns every online node on the tailnet, self included.
func (c *TailscaleClient) Hosts(ctx context.Context) ([]TailnetHost, error) {
	st, err := c.lc.Status(ctx)
	if err != nil {
		return nil, err
	}

	var hosts []TailnetHost
	if st.Self != nil && st.Self.Online {
		hosts = append(hosts, TailnetHost{
			Hostname: shortHostname(st.Self.HostName),
			FQDN:     trimDot(st.Self.DNSName),
			IPs:      st.Self.TailscaleIPs,
			IsSelf:   true,
		})
	}
	for _, p := range st.Peer {
		if !p.Online {
			continue
		}
		hosts = append(hosts, TailnetHost{
			Hostname: shortHostname(p.HostName),
			FQDN:     trimDot(p.DNSName),
			IPs:      p.TailscaleIPs,
		})
	}
	return hosts, nil
}

// SelfHostname returns this node's short display hostname, used to key
// Docker-discovered (i.e. always-local) services under the right host.
func (c *TailscaleClient) SelfHostname(ctx context.Context) (string, error) {
	st, err := c.lc.Status(ctx)
	if err != nil {
		return "", err
	}
	if st.Self == nil {
		return "", nil
	}
	return shortHostname(st.Self.HostName), nil
}

// SelfFQDN returns this node's full tailnet DNS name, used to build URLs and
// TLS SNI for Docker-discovered services so they match what a browser sees.
func (c *TailscaleClient) SelfFQDN(ctx context.Context) (string, error) {
	st, err := c.lc.Status(ctx)
	if err != nil {
		return "", err
	}
	if st.Self == nil {
		return "", nil
	}
	return trimDot(st.Self.DNSName), nil
}

// shortHostname is HostInfo's Hostname field, not a DNS name or necessarily
// unique — only fit for display/grouping, never for building a URL.
func shortHostname(h string) string {
	return strings.TrimSuffix(h, ".")
}

// trimDot strips the trailing dot Tailscale's FQDN (DNSName) always includes.
func trimDot(h string) string {
	return strings.TrimSuffix(h, ".")
}
