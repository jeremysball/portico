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
	Interval             time.Duration // how often to rescan local Docker containers (default 5s)
	TailnetInterval      time.Duration // how often to recheck already-known tailnet services (default 30s; probes only their exact host:port, so this stays cheap regardless of Ports' size)
	TailnetSweepInterval time.Duration // how often to fan out the full Ports x hosts scan looking for new tailnet services (default 6h; deliberately rare since this is the expensive pass — new services are normally found via the manual per-host refresh button instead)
	IdentifyInterval     time.Duration // how often to run nmap service/version identification against already-online services (default 6h; deliberately rare, this is not a scan)
	ProbeTimeout         time.Duration // per-request HTTP timeout (default 1.5s)
	Concurrency          int           // max in-flight probes (default 40)
	Ports                []int         // common ports to probe on every tailnet host
	DockerSocket         string
	TailscaleSocket      string
}

// Orchestrator ties the discovery sources together and keeps a
// registry.Registry up to date. Docker (cheap, local) runs on its own
// ticker. Tailnet probing is split into two: a frequent recheck of services
// already known (cheap — one probe per known host:port) and a rare full
// sweep of every port against every peer (expensive — the only pass that
// discovers brand-new services, so it stays automatic without needing the
// recheck's traffic volume). A separate, much slower ticker opportunistically
// identifies what's actually running on already-discovered services via nmap.
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
	go o.loop(ctx, o.cfg.TailnetInterval, o.recheckPass)
	o.loop(ctx, o.cfg.TailnetSweepInterval, o.sweepPass)
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
	host   string // short display hostname
	fqdn   string // full DNS hostname for URL/SNI, may be empty (falls back to host)
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
	fqdn   string
	addr   string
	port   int
	res    *ProbeResult
	docker *DockerContainer
}

// selfHostname resolves this node's short display hostname: the tailnet
// hostname when available (cheap local IPC, not a network probe), else the OS
// hostname. Used for grouping/category, never for building URLs.
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

// selfFQDN resolves this node's full tailnet DNS name, used to build URLs and
// TLS SNI for Docker-discovered services so they match what a browser sees.
// Returns "" when tailscale isn't available; callers fall back to the short
// hostname in that case.
func (o *Orchestrator) selfFQDN(ctx context.Context) string {
	if o.tailnet.Available(ctx) {
		if fq, err := o.tailnet.SelfFQDN(ctx); err == nil {
			return fq
		}
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
	selfFQDN := o.selfFQDN(passCtx)

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
			targets[key] = &target{host: selfHost, fqdn: selfFQDN, addr: "localhost", port: port, docker: &c}
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
		if res, ok := o.prober.ProbeScheme(passCtx, es.scheme, es.host, es.addr, es.port); ok {
			resultsMu.Lock()
			results = append(results, probedResult{id: id, source: "docker", host: es.host, fqdn: es.host, addr: es.addr, port: es.port, res: res, docker: es.docker})
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
			res, ok := o.prober.Probe(passCtx, t.fqdn, t.addr, t.port)
			if !ok {
				return
			}
			id := dockerID(t.host, t.port)
			resultsMu.Lock()
			results = append(results, probedResult{id: id, source: "docker", host: t.host, fqdn: t.fqdn, addr: t.addr, port: t.port, res: res, docker: t.docker})
			resultsMu.Unlock()
		}()
	}
	wg.Wait()

	for _, r := range dedupeSameService(results) {
		o.upsertFromProbe(r.id, r.source, r.host, r.fqdn, r.addr, r.port, r.res, r.docker)
		markSeen(r.id)
	}

	o.reg.MarkOfflineExceptForSource("docker", seen)
}

// recheckPass reprobes only the tailnet services already sitting in the
// registry, one request per known host:port. This is what runs on the
// frequent TailnetInterval and owns marking tailnet services offline: since
// it always covers every currently-known service, a service missing from its
// results is actually down, not just skipped by a rarer pass.
func (o *Orchestrator) recheckPass(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	known := o.reg.ListSource("tailscale")
	if len(known) == 0 {
		return
	}

	var results []probedResult
	var resultsMu sync.Mutex

	sem := make(chan struct{}, max(1, o.cfg.Concurrency))
	var wg sync.WaitGroup
	for _, s := range known {
		s := s
		wg.Add(1)
		sem <- struct{}{}
		go func() {
			defer wg.Done()
			defer func() { <-sem }()
			res, ok := o.prober.Probe(passCtx, s.FQDN, s.Address, s.Port)
			if !ok {
				return
			}
			resultsMu.Lock()
			results = append(results, probedResult{id: s.ID, source: "tailscale", host: s.Host, fqdn: s.FQDN, addr: s.Address, port: s.Port, res: res})
			resultsMu.Unlock()
		}()
	}
	wg.Wait()

	seen := make(map[string]struct{}, len(results))
	for _, r := range results {
		o.upsertFromProbe(r.id, r.source, r.host, r.fqdn, r.addr, r.port, r.res, r.docker)
		seen[r.id] = struct{}{}
	}

	o.reg.MarkOfflineExceptForSource("tailscale", seen)
}

// sweepPass probes every configured port on every online tailnet host,
// including this one. This is the expensive fan-out (Ports x hosts HTTP
// requests each pass) that discovers brand-new services, so it runs on the
// much slower TailnetSweepInterval; recheckPass, not this pass, is what
// keeps already-known services' online status current between sweeps.
func (o *Orchestrator) sweepPass(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if !o.tailnet.Available(passCtx) {
		return
	}
	hosts, err := o.tailnet.Hosts(passCtx)
	if err != nil {
		o.log.Warn("tailscale status failed", "err", err)
		return
	}

	for _, r := range o.probeHosts(passCtx, hosts, o.cfg.Ports) {
		o.upsertFromProbe(r.id, r.source, r.host, r.fqdn, r.addr, r.port, r.res, r.docker)
	}
}

// SweepHost runs a full Ports scan against a single named tailnet host,
// on demand — the manual-refresh counterpart to sweepPass, for a user who
// knows exactly which peer just started running a new service and doesn't
// want to wait for the next scheduled sweep.
func (o *Orchestrator) SweepHost(ctx context.Context, host string) error {
	passCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	if !o.tailnet.Available(passCtx) {
		return fmt.Errorf("tailscale not available")
	}
	hosts, err := o.tailnet.Hosts(passCtx)
	if err != nil {
		return fmt.Errorf("tailscale status: %w", err)
	}

	var matched *TailnetHost
	for i := range hosts {
		if hosts[i].Hostname == host {
			matched = &hosts[i]
			break
		}
	}
	if matched == nil {
		return fmt.Errorf("host %q not found on tailnet", host)
	}

	results := o.probeHosts(passCtx, []TailnetHost{*matched}, o.cfg.Ports)
	seen := make(map[string]struct{}, len(results))
	for _, r := range results {
		o.upsertFromProbe(r.id, r.source, r.host, r.fqdn, r.addr, r.port, r.res, r.docker)
		seen[r.id] = struct{}{}
	}
	o.reg.MarkOfflineExceptForHostSource("tailscale", host, seen)
	return nil
}

// probeHosts fans out an HTTP probe for every port against every host,
// deduped the same way a regular tailnet pass is. Shared by sweepPass (all
// hosts) and SweepHost (a single host).
func (o *Orchestrator) probeHosts(ctx context.Context, hosts []TailnetHost, ports []int) []probedResult {
	targets := map[string]*target{}
	for _, h := range hosts {
		if len(h.IPs) == 0 {
			continue
		}
		hostname := h.Hostname
		fqdn := h.FQDN
		addr := h.IPs[0].String()
		if hostname == "" {
			hostname = addr
		}
		for _, port := range ports {
			key := fmt.Sprintf("%s|%d", addr, port)
			if _, ok := targets[key]; ok {
				continue
			}
			targets[key] = &target{host: hostname, fqdn: fqdn, addr: addr, port: port}
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
			res, ok := o.prober.Probe(ctx, t.fqdn, t.addr, t.port)
			if !ok {
				return
			}
			id := fmt.Sprintf("%s:%d", t.host, t.port)
			resultsMu.Lock()
			results = append(results, probedResult{id: id, source: "tailscale", host: t.host, fqdn: t.fqdn, addr: t.addr, port: t.port, res: res})
			resultsMu.Unlock()
		}()
	}
	wg.Wait()

	return dedupeSameService(results)
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

func (o *Orchestrator) upsertFromProbe(id, source, host, fqdn, addr string, port int, res *ProbeResult, dc *DockerContainer) {
	urlHost := fqdn
	if urlHost == "" {
		urlHost = host
	}
	svc := registry.Service{
		ID:       id,
		Host:     host,
		FQDN:     fqdn,
		Address:  addr,
		Port:     port,
		Scheme:   res.Scheme,
		URL:      fmt.Sprintf("%s://%s:%d/", res.Scheme, urlHost, port),
		Title:    res.Title,
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
		FQDN:     es.host,
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
