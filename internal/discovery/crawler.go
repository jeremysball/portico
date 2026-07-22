package discovery

import (
	"context"
	"net/netip"
)

// Host is a reachable machine discovered by a Crawler.
type Host struct {
	Name string
	FQDN string
	IPs  []netip.Addr
	Self bool
}

// Crawler discovers hosts from an external source.
type Crawler interface {
	Source() string
	Discover(ctx context.Context) ([]Host, error)
}
