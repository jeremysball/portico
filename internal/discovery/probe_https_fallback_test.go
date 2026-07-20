package discovery

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// TestProbe_FallsBackToHTTPSOnNonStandardPort reproduces the real-world
// symptom: a TLS-only service on a port outside the {443,8443,9443}
// fast-path list. Go's TLS server answers a plaintext GET with a
// well-formed 400 body ("Client sent an HTTP request to an HTTPS
// server."), which used to get accepted as a live http service.
func TestProbe_FallsBackToHTTPSOnNonStandardPort(t *testing.T) {
	ts := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("<title>Secret TLS Service</title>"))
	}))
	defer ts.Close()

	host := strings.TrimPrefix(ts.URL, "https://")
	addr, portStr, _ := strings.Cut(host, ":")
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	p := NewProber(2 * time.Second)
	res, ok := p.Probe(context.Background(), "", addr, port)
	if !ok {
		t.Fatal("expected probe to succeed")
	}
	if res.Scheme != "https" {
		t.Fatalf("expected scheme=https, got %q", res.Scheme)
	}
	if res.Title != "Secret TLS Service" {
		t.Fatalf("expected title scraped from the https response, got %q", res.Title)
	}
}
