package discovery

import (
	"context"
	"crypto/tls"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	neturl "net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var titleRe = regexp.MustCompile(`(?is)<title[^>]*>\s*(.*?)\s*</title>`)

// ProbeResult describes a live HTTP(S) endpoint found at a given address/port.
type ProbeResult struct {
	Scheme string
	Title  string
}

// Prober performs lightweight HTTP(S) liveness checks against host:port pairs.
type Prober struct {
	client *http.Client
}

// dialAddrKey carries the real dial target (IP or "localhost") through a
// request's context, so the request URL can use the display/DNS hostname
// (for correct TLS SNI and Host header) while the TCP connection still goes
// to the address the caller actually resolved, rather than depending on that
// hostname resolving via DNS itself.
type dialAddrKey struct{}

func NewProber(timeout time.Duration) *Prober {
	dialer := &net.Dialer{Timeout: timeout}
	return &Prober{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
					if override, ok := ctx.Value(dialAddrKey{}).(string); ok && override != "" {
						address = override
					}
					return dialer.DialContext(ctx, network, address)
				},
			},
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				if len(via) >= 5 {
					return http.ErrUseLastResponse
				}
				return nil
			},
		},
	}
}

// Probe checks whether an HTTP(S) service is listening at addr:port. host is
// the display/DNS hostname used to build the request URL — and therefore the
// TLS SNI and Host header — so the probe sees the same virtual host a real
// browser visiting that hostname would; pass "" when no better hostname than
// addr is known. Any response at all (including 401/403/redirects) counts as
// "up" — plenty of self-hosted dashboards require auth on "/".
func (p *Prober) Probe(ctx context.Context, host, addr string, port int) (*ProbeResult, bool) {
	schemes := []string{"http", "https"}
	if port == 443 || port == 8443 || port == 9443 {
		schemes = []string{"https", "http"}
	}
	for _, scheme := range schemes {
		if res, ok := p.ProbeScheme(ctx, scheme, host, addr, port); ok {
			return res, true
		}
	}
	return nil, false
}

// ProbeScheme checks a single explicit scheme (used when a caller already
// knows which one applies, e.g. a docker label declaring "https://...").
func (p *Prober) ProbeScheme(ctx context.Context, scheme, host, addr string, port int) (*ProbeResult, bool) {
	if host == "" {
		host = addr
	}
	base := fmt.Sprintf("%s://%s:%d/", scheme, host, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return nil, false
	}
	req = req.WithContext(context.WithValue(req.Context(), dialAddrKey{}, fmt.Sprintf("%s:%d", addr, port)))
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer func() { _ = resp.Body.Close() }()

	// A response that landed (after following redirects) on the *same* host
	// but a *different* port is just a redirect stub — e.g. :80 bouncing to
	// :443 on the same box — not a distinct service. Skip it so it doesn't
	// produce a duplicate tile alongside the port it actually redirects to;
	// that port gets probed and shown on its own. A redirect to a different
	// hostname entirely is left alone (could be a legitimate canonical-domain
	// redirect rather than a same-box stub), even though that's an
	// imperfect heuristic in either direction.
	if resp.Request != nil && resp.Request.URL != nil {
		gotHost, gotPort := hostPort(resp.Request.URL)
		if gotHost == host && gotPort != port {
			return nil, false
		}
	}

	// 404 and 502 mean there's clearly nothing real behind this port — a
	// catch-all vhost or a reverse proxy with no live backend, not an actual
	// service. Everything else (including 401/403 — plenty of self-hosted
	// dashboards require auth on "/") still counts as "up".
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusBadGateway {
		return nil, false
	}

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	// A plaintext GET against a TLS listener doesn't fail the connection —
	// Go's net/http server (and several others, e.g. nginx's default TLS
	// vhost) answers with a well-formed plaintext 400 whose body says so.
	// That looks like a legitimate "up" response and would otherwise get
	// recorded as scheme=http, permanently masking the real https service.
	if scheme == "http" && resp.StatusCode == http.StatusBadRequest &&
		strings.Contains(string(body), "Client sent an HTTP request to an HTTPS server") {
		return nil, false
	}

	// Only trust the scraped title on success responses — error/auth pages
	// (401/403/404...) often carry a generic <title> ("Not Found",
	// "Unauthorized") that would otherwise get shown as the service's name.
	title := ""
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if m := titleRe.FindSubmatch(body); m != nil {
			title = strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
		}
	}

	return &ProbeResult{Scheme: scheme, Title: title}, true
}

// hostPort splits a URL into hostname and effective port, filling in the
// scheme's default port (80/443) when the URL omitted an explicit one —
// which is exactly what a bare "Location: https://host/" redirect does.
func hostPort(u *neturl.URL) (string, int) {
	h := u.Hostname()
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err == nil {
			return h, n
		}
	}
	if u.Scheme == "https" {
		return h, 443
	}
	return h, 80
}
