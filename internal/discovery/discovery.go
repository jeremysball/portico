package discovery

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"strconv"
	"sync"
	"time"

	"github.com/jeremysball/portico/internal/registry"
)

// Config controls the discovery orchestrator.
type Config struct {
	Interval        time.Duration // how often to rescan local Docker containers (default 5s)
	TailnetInterval time.Duration // how often to probe tailnet peers (default 30s; each pass fans out Ports x hosts requests, so this runs far less often than Interval)
	ProbeTimeout    time.Duration // per-request HTTP timeout (default 1.5s)
	Concurrency     int           // max in-flight probes (default 40)
	Ports           []int         // common ports to probe on every tailnet host
	DockerSocket    string
	TailscaleSocket string
}

// Orchestrator ties the three discovery sources together and keeps a
// registry.Registry up to date. Docker (cheap, local) and tailnet (expensive,
// cross-network) run on independent tickers so a large tailnet doesn't force
// local updates to lag, and so tailnet peers aren't hammered as often.
type Orchestrator struct {
	cfg     Config
	docker  *DockerClient
	tailnet *TailscaleClient
	prober  *Prober
	reg     *registry.Registry
	log     *slog.Logger
}

func NewOrchestrator(cfg Config, reg *registry.Registry, log *slog.Logger) *Orchestrator {
	return &Orchestrator{
		cfg:     cfg,
		docker:  NewDockerClient(cfg.DockerSocket),
		tailnet: NewTailscaleClient(cfg.TailscaleSocket),
		prober:  NewProber(cfg.ProbeTimeout),
		reg:     reg,
		log:     log,
	}
}

// Run blocks, scanning both sources immediately and then on their own
// intervals until ctx is done.
func (o *Orchestrator) Run(ctx context.Context) {
	go o.loop(ctx, o.cfg.Interval, o.dockerPass)
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

// target is a single (address, port) to probe, optionally enriched with
// Docker label metadata.
type target struct {
	host   string // display hostname
	addr   string // dial address
	port   int
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

	for _, es := range explicit {
		id := dockerID(es.host, es.port)
		if res, ok := o.prober.ProbeScheme(passCtx, es.scheme, es.addr, es.port); ok {
			o.upsertFromProbe(id, "docker", es.host, es.addr, es.port, res, es.docker)
		} else {
			// Trust the label even if the probe failed (e.g. odd auth); still
			// worth showing so the user knows it's configured.
			o.upsertExplicit(id, es)
		}
		markSeen(id)
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
			id := dockerID(t.host, t.port)
			res, ok := o.prober.Probe(passCtx, t.addr, t.port)
			if !ok {
				return
			}
			markSeen(id)
			o.upsertFromProbe(id, "docker", t.host, t.addr, t.port, res, t.docker)
		}()
	}
	wg.Wait()

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

	seen := make(map[string]struct{}, len(targets))
	var seenMu sync.Mutex

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
			seenMu.Lock()
			seen[id] = struct{}{}
			seenMu.Unlock()
			o.upsertFromProbe(id, "tailscale", t.host, t.addr, t.port, res, nil)
		}()
	}
	wg.Wait()

	o.reg.MarkOfflineExceptForSource("tailscale", seen)
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
