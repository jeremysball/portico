package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/jeremysball/portico/internal/registry"
)

// Config controls the discovery orchestrator.
type Config struct {
	Interval         time.Duration // how often to rescan local Docker containers (default 5s)
	TailnetInterval  time.Duration // how often to probe tailnet peers (default 30s; each pass fans out Ports x hosts requests, so this runs far less often than Interval)
	IdentifyInterval time.Duration // how often to run nmap service/version identification against already-online services (default 6h; deliberately rare, this is not a scan)
	ProbeTimeout     time.Duration // per-request HTTP timeout (default 1.5s)
	Concurrency      int           // max in-flight probes (default 40)
	Ports            []int         // common ports to probe on every tailnet host
	DockerSocket     string
	TailscaleSocket  string
}

// Orchestrator ties the discovery sources together and keeps a
// registry.Registry up to date. Docker (cheap, local) and tailnet (expensive,
// cross-network) run on independent tickers so a large tailnet doesn't force
// local updates to lag, and so tailnet peers aren't hammered as often. A
// third, much slower ticker opportunistically identifies what's actually
// running on already-discovered services via nmap.
type Orchestrator struct {
	cfg           Config
	docker        *DockerClient
	tailnet       *TailscaleClient
	prober        *Prober
	reg           *registry.Registry
	log           *slog.Logger
	nmapAvailable bool
}

func NewOrchestrator(cfg Config, reg *registry.Registry, log *slog.Logger) *Orchestrator {
	_, nmapErr := exec.LookPath("nmap")
	return &Orchestrator{
		cfg:           cfg,
		docker:        NewDockerClient(cfg.DockerSocket),
		tailnet:       NewTailscaleClient(cfg.TailscaleSocket),
		prober:        NewProber(cfg.ProbeTimeout),
		reg:           reg,
		log:           log,
		nmapAvailable: nmapErr == nil,
	}
}

// Run blocks, scanning both sources immediately and then on their own
// intervals until ctx is done.
func (o *Orchestrator) Run(ctx context.Context) {
	if !o.nmapAvailable {
		o.log.Info("nmap not found; service/version identification disabled")
	}
	go o.loop(ctx, o.cfg.Interval, o.dockerPass)
	go o.loopDelayed(ctx, 2*time.Minute, o.cfg.IdentifyInterval, o.identifyPass)
	o.loop(ctx, o.cfg.TailnetInterval, o.tailnetPass)
}

func (o *Orchestrator) loop(ctx context.Context, interval time.Duration, pass func(context.Context)) {
	pass(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass(ctx)
		}
	}
}

// loopDelayed is like loop but waits startDelay before its first run, so a
// pass that depends on other discovery having already populated the
// registry (e.g. identification) doesn't fire against an empty registry
// at startup.
func (o *Orchestrator) loopDelayed(ctx context.Context, startDelay, interval time.Duration, pass func(context.Context)) {
	select {
	case <-ctx.Done():
		return
	case <-time.After(startDelay):
	}
	pass(ctx)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			pass(ctx)
		}
	}
}

// target is a single (address, port) to probe, optionally enriched with
// Docker label metadata.
type target struct {
	host   string // display hostname
	addr   string // dial address
	port   int
	docker *DockerContainer
}

// probedResult is a successful probe awaiting dedup before it's committed
// to the registry.
type probedResult struct {
	id     string
	source string
	host   string
	addr   string
	port   int
	res    *ProbeResult
	docker *DockerContainer
}

// selfHostname resolves this node's display hostname: the tailnet hostname
// when available (cheap local IPC, not a network probe), else the OS hostname.
func (o *Orchestrator) selfHostname(ctx context.Context) string {
	if h, err := os.Hostname(); err == nil {
		if o.tailnet.Available(ctx) {
			if th, err := o.tailnet.SelfHostname(ctx); err == nil && th != "" {
				return th
			}
		}
		return h
	}
	return ""
}

// dockerPass discovers containers running on this host and probes their
// published ports (or an explicit portico.url label). Docker is always
// local, so this is cheap and safe to run frequently.
func (o *Orchestrator) dockerPass(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	if !o.docker.Available(passCtx) {
		return
	}

	selfHost := o.selfHostname(passCtx)

	containers, err := o.docker.List(passCtx)
	if err != nil {
		o.log.Warn("docker list failed", "err", err)
		return
	}

	var explicit []explicitService
	targets := map[string]*target{}
	for i := range containers {
		c := containers[i]
		if raw := c.ExplicitURL(); raw != "" {
			if es, ok := parseExplicitURL(raw, c); ok {
				explicit = append(explicit, es)
			}
			continue
		}
		for _, port := range c.Ports {
			key := fmt.Sprintf("%d", port)
			if existing, ok := targets[key]; ok {
				existing.docker = &c
				continue
			}
			targets[key] = &target{host: selfHost, addr: "localhost", port: port, docker: &c}
		}
	}

	seen := make(map[string]struct{}, len(targets)+len(explicit))
	var seenMu sync.Mutex
	markSeen := func(id string) {
		seenMu.Lock()
		seen[id] = struct{}{}
		seenMu.Unlock()
	}

	var results []probedResult
	var resultsMu sync.Mutex

	for _, es := range explicit {
		id := dockerID(es.host, es.port)
		if res, ok := o.prober.ProbeScheme(passCtx, es.scheme, es.addr, es.port); ok {
			resultsMu.Lock()
			results = append(results, probedResult{id: id, source: "docker", host: es.host, addr: es.addr, port: es.port, res: res, docker: es.docker})
			resultsMu.Unlock()
		} else {
			// Trust the label even if the probe failed (e.g. odd auth); still
			// worth showing so the user knows it's configured. Bypasses
			// dedup entirely since there's no scraped title to compare.
			o.upsertExplicit(id, es)
			markSeen(id)
		}
	}

	sem := make(chan struct{}, max(1, o.cfg.Concurrency))
	var wg sync.WaitGroup
	for _, t := range targets {
		t := t
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, ok := o.prober.Probe(passCtx, t.addr, t.port)
			if !ok {
				return
			}
			id := dockerID(t.host, t.port)
			resultsMu.Lock()
			results = append(results, probedResult{id: id, source: "docker", host: t.host, addr: t.addr, port: t.port, res: res, docker: t.docker})
			resultsMu.Unlock()
		}()
	}
	wg.Wait()

	for _, r := range dedupeSameService(results) {
		o.upsertFromProbe(r.id, r.source, r.host, r.addr, r.port, r.res, r.docker)
		markSeen(r.id)
	}

	o.reg.MarkOfflineExceptForSource("docker", seen)
}

// tailnetPass probes every configured port on every online tailnet host,
// including this one. This is the expensive fan-out (Ports x hosts HTTP
// requests each pass), so it runs on the slower TailnetInterval.
func (o *Orchestrator) tailnetPass(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	if !o.tailnet.Available(passCtx) {
		return
	}
	hosts, err := o.tailnet.Hosts(passCtx)
	if err != nil {
		o.log.Warn("tailscale status failed", "err", err)
		return
	}

	targets := map[string]*target{}
	for _, h := range hosts {
		if len(h.IPs) == 0 {
			continue
		}
		hostname := h.Hostname
		addr := h.IPs[0].String()
		if hostname == "" {
			hostname = addr
		}
		for _, port := range o.cfg.Ports {
			key := fmt.Sprintf("%s|%d", addr, port)
			if _, ok := targets[key]; ok {
				continue
			}
			targets[key] = &target{host: hostname, addr: addr, port: port}
		}
	}

	var results []probedResult
	var resultsMu sync.Mutex

	sem := make(chan struct{}, max(1, o.cfg.Concurrency))
	var wg sync.WaitGroup
	for _, t := range targets {
		t := t
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, ok := o.prober.Probe(passCtx, t.addr, t.port)
			if !ok {
				return
			}
			id := fmt.Sprintf("%s:%d", t.host, t.port)
			resultsMu.Lock()
			results = append(results, probedResult{id: id, source: "tailscale", host: t.host, addr: t.addr, port: t.port, res: res})
			resultsMu.Unlock()
		}()
	}
	wg.Wait()

	deduped := dedupeSameService(results)
	seen := make(map[string]struct{}, len(deduped))
	for _, r := range deduped {
		o.upsertFromProbe(r.id, r.source, r.host, r.addr, r.port, r.res, r.docker)
		seen[r.id] = struct{}{}
	}

	o.reg.MarkOfflineExceptForSource("tailscale", seen)
}

// dedupeSameService drops results that are just a same-host duplicate of
// another result with an identical scraped title — e.g. the same app
// answering on both :80 and :443 without one redirecting to the other, which
// the redirect-stub check in probe.go can't catch since both are genuinely
// live responses. Results with no scraped title are never deduped, since an
// empty title isn't a reliable signal that two ports are the same service.
// Between duplicates, https wins, then the lower port.
func dedupeSameService(results []probedResult) []probedResult {
	type key struct{ host, title string }
	bestIdx := map[key]int{}
	keyOf := make([]key, len(results))
	for i, r := range results {
		title := strings.TrimSpace(r.res.Title)
		if title == "" {
			continue // sentinel zero-value key below; never deduped
		}
		k := key{host: r.host, title: title}
		keyOf[i] = k
		j, ok := bestIdx[k]
		if !ok || isBetterDuplicate(results[i], results[j]) {
			bestIdx[k] = i
		}
	}

	out := make([]probedResult, 0, len(results))
	for i, r := range results {
		k := keyOf[i]
		if k == (key{}) || bestIdx[k] == i {
			out = append(out, r)
		}
	}
	return out
}

func isBetterDuplicate(a, b probedResult) bool {
	aHTTPS, bHTTPS := a.res.Scheme == "https", b.res.Scheme == "https"
	if aHTTPS != bHTTPS {
		return aHTTPS
	}
	return a.port < b.port
}

// dockerID namespaces Docker-sourced IDs so they can never collide with a
// tailnet-sourced entry for the same host:port — notably when this machine's
// own tailnet hostname (self) is scanned alongside its own Docker containers.
// Without this, two goroutines could race to upsert the same registry key
// from two different services, flipping the displayed title pass to pass.
func dockerID(host string, port int) string {
	return fmt.Sprintf("docker:%s:%d", host, port)
}

func (o *Orchestrator) upsertFromProbe(id, source, host, addr string, port int, res *ProbeResult, dc *DockerContainer) {
	svc := registry.Service{
		ID:       id,
		Host:     host,
		Address:  addr,
		Port:     port,
		Scheme:   res.Scheme,
		URL:      fmt.Sprintf("%s://%s:%d/", res.Scheme, host, port),
		Title:    res.Title,
		Icon:     res.Icon,
		Source:   source,
		Category: host,
	}
	if dc != nil {
		if dc.NameOverride() != "" {
			svc.Title = dc.NameOverride()
		} else if dc.Name != "" && svc.Title == "" {
			svc.Title = dc.Name
		}
		if dc.IconOverride() != "" {
			svc.Icon = dc.IconOverride()
		}
		if dc.Category() != "" {
			svc.Category = dc.Category()
		}
	}
	o.reg.Upsert(svc)
}

type explicitService struct {
	host   string
	addr   string
	port   int
	scheme string
	docker *DockerContainer
}

func (o *Orchestrator) upsertExplicit(id string, es explicitService) {
	svc := registry.Service{
		ID:       id,
		Host:     es.host,
		Address:  es.addr,
		Port:     es.port,
		Scheme:   es.scheme,
		Source:   "docker",
		Category: es.host,
	}
	svc.URL = fmt.Sprintf("%s://%s:%d/", es.scheme, es.host, es.port)
	if es.docker != nil {
		if es.docker.NameOverride() != "" {
			svc.Title = es.docker.NameOverride()
		} else {
			svc.Title = es.docker.Name
		}
		if es.docker.IconOverride() != "" {
			svc.Icon = es.docker.IconOverride()
		}
		if es.docker.Category() != "" {
			svc.Category = es.docker.Category()
		}
	}
	o.reg.Upsert(svc)
}

func parseExplicitURL(raw string, c DockerContainer) (explicitService, bool) {
	u, err := url.Parse(raw)
	if err != nil || u.Hostname() == "" {
		return explicitService{}, false
	}
	scheme := u.Scheme
	if scheme == "" {
		scheme = "http"
	}
	portStr := u.Port()
	port := 80
	if scheme == "https" {
		port = 443
	}
	if portStr != "" {
		if p, err := strconv.Atoi(portStr); err == nil {
			port = p
		}
	}
	return explicitService{
		host:   u.Hostname(),
		addr:   u.Hostname(),
		port:   port,
		scheme: scheme,
		docker: &c,
	}, true
}

// identifyPass opportunistically runs nmap service/version detection against
// services already confirmed online by the HTTP probes above. It never scans
// a port range itself — only single, already-known-open ports — and runs on
// a far slower cadence (IdentifyInterval, default 6h), so it stays cheap
// regardless of how it's implemented.
func (o *Orchestrator) identifyPass(ctx context.Context) {
	if !o.nmapAvailable {
		return
	}
	passCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()

	services := o.reg.List()

	sem := make(chan struct{}, 5)
	var wg sync.WaitGroup
	for _, s := range services {
		if !s.Online {
			continue
		}
		s := s
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			detected, ok := identifyService(passCtx, s.Address, s.Port)
			if ok {
				o.reg.SetDetected(s.ID, detected)
			}
		}()
	}
	wg.Wait()
}
