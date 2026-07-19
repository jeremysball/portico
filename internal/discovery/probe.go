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
	"strings"
	"time"
)

var (
	titleRe    = regexp.MustCompile(`(?is)<title[^>]*>\s*(.*?)\s*</title>`)
	iconLinkRe = regexp.MustCompile(`(?is)<link[^>]+rel=["']?(?:shortcut icon|icon|apple-touch-icon)["']?[^>]*href=["']([^"'>]+)["']`)
)

// ProbeResult describes a live HTTP(S) endpoint found at a given address/port.
type ProbeResult struct {
	Scheme string
	Title  string
	Icon   string // absolute URL, empty if none could be found
}

// Prober performs lightweight HTTP(S) liveness checks against host:port pairs.
type Prober struct {
	client *http.Client
}

func NewProber(timeout time.Duration) *Prober {
	return &Prober{
		client: &http.Client{
			Timeout: timeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true},
				DialContext:     (&net.Dialer{Timeout: timeout}).DialContext,
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

// Probe checks whether an HTTP(S) service is listening at addr:port. Any
// response at all (including 401/403/redirects) counts as "up" — plenty of
// self-hosted dashboards require auth on "/".
func (p *Prober) Probe(ctx context.Context, addr string, port int) (*ProbeResult, bool) {
	schemes := []string{"http", "https"}
	if port == 443 || port == 8443 || port == 9443 {
		schemes = []string{"https", "http"}
	}
	for _, scheme := range schemes {
		if res, ok := p.ProbeScheme(ctx, scheme, addr, port); ok {
			return res, true
		}
	}
	return nil, false
}

// ProbeScheme checks a single explicit scheme (used when a caller already
// knows which one applies, e.g. a docker label declaring "https://...").
func (p *Prober) ProbeScheme(ctx context.Context, scheme, addr string, port int) (*ProbeResult, bool) {
	base := fmt.Sprintf("%s://%s:%d/", scheme, addr, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return nil, false
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 64*1024))

	// Only trust the scraped title on success responses — error/auth pages
	// (401/403/404...) often carry a generic <title> ("Not Found",
	// "Unauthorized") that would otherwise get shown as the service's name.
	title := ""
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		if m := titleRe.FindSubmatch(body); m != nil {
			title = strings.Join(strings.Fields(html.UnescapeString(string(m[1]))), " ")
		}
	}

	icon := extractIcon(base, body)
	if icon == "" {
		icon = p.probeDefaultFavicon(ctx, scheme, addr, port)
	}

	return &ProbeResult{Scheme: scheme, Title: title, Icon: icon}, true
}

func extractIcon(baseURL string, body []byte) string {
	m := iconLinkRe.FindSubmatch(body)
	if m == nil {
		return ""
	}
	href := strings.TrimSpace(string(m[1]))
	rel, err := neturl.Parse(href)
	if err != nil {
		return ""
	}
	base, err := neturl.Parse(baseURL)
	if err != nil {
		return ""
	}
	return base.ResolveReference(rel).String()
}

// probeDefaultFavicon does a quick existence check for /favicon.ico so the UI
// doesn't render a broken-image icon when no <link rel="icon"> was declared.
func (p *Prober) probeDefaultFavicon(ctx context.Context, scheme, addr string, port int) string {
	url := fmt.Sprintf("%s://%s:%d/favicon.ico", scheme, addr, port)
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return ""
	}
	resp, err := p.client.Do(req)
	if err != nil {
		return ""
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return url
	}
	return ""
}
