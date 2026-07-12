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
	Interval        time.Duration // how often to re-scan (default 5s)
	ProbeTimeout    time.Duration // per-request HTTP timeout (default 1.5s)
	Concurrency     int           // max in-flight probes (default 40)
	Ports           []int         // common ports to probe on every tailnet host
	DockerSocket    string
	TailscaleSocket string
}

// Orchestrator ties the three discovery sources together and keeps a
// registry.Registry up to date on a fixed interval.
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

// Run blocks, scanning immediately and then every cfg.Interval until ctx is done.
func (o *Orchestrator) Run(ctx context.Context) {
	o.runOnce(ctx)
	ticker := time.NewTicker(o.cfg.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			o.runOnce(ctx)
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

func (o *Orchestrator) runOnce(ctx context.Context) {
	passCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()

	selfHost := ""
	if h, err := os.Hostname(); err == nil {
		selfHost = h
	}

	var tailnetHosts []TailnetHost
	if o.tailnet.Available(passCtx) {
		if hosts, err := o.tailnet.Hosts(passCtx); err == nil {
			tailnetHosts = hosts
		} else {
			o.log.Warn("tailscale status failed", "err", err)
		}
		if h, err := o.tailnet.SelfHostname(passCtx); err == nil && h != "" {
			selfHost = h
		}
	}

	targets := map[string]*target{}
	addTarget := func(host, addr string, port int, dc *DockerContainer) {
		key := fmt.Sprintf("%s|%d", addr, port)
		if existing, ok := targets[key]; ok {
			if dc != nil {
				existing.docker = dc
			}
			return
		}
		targets[key] = &target{host: host, addr: addr, port: port, docker: dc}
	}

	for _, h := range tailnetHosts {
		if len(h.IPs) == 0 {
			continue
		}
		hostname := h.Hostname
		addr := h.IPs[0].String()
		if hostname == "" {
			hostname = addr
		}
		for _, port := range o.cfg.Ports {
			addTarget(hostname, addr, port, nil)
		}
	}

	// Docker containers are always local to this machine; dial them via
	// localhost (the portico container is expected to run with host
	// networking so it can reach sibling containers' published ports).
	var explicit []explicitService
	if o.docker.Available(passCtx) {
		containers, err := o.docker.List(passCtx)
		if err != nil {
			o.log.Warn("docker list failed", "err", err)
		}
		for i := range containers {
			c := containers[i]
			if raw := c.ExplicitURL(); raw != "" {
				if es, ok := parseExplicitURL(raw, c); ok {
					explicit = append(explicit, es)
				}
				continue
			}
			for _, port := range c.Ports {
				addTarget(selfHost, "localhost", port, &c)
			}
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
		id := fmt.Sprintf("%s:%d", es.host, es.port)
		if res, ok := o.prober.ProbeScheme(passCtx, es.scheme, es.addr, es.port); ok {
			o.upsertFromProbe(id, es.host, es.addr, es.port, res, es.docker)
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
			id := fmt.Sprintf("%s:%d", t.host, t.port)
			res, ok := o.prober.Probe(passCtx, t.addr, t.port)
			if !ok {
				return
			}
			markSeen(id)
			o.upsertFromProbe(id, t.host, t.addr, t.port, res, t.docker)
		}()
	}
	wg.Wait()

	o.reg.MarkOfflineExcept(seen)
}

func (o *Orchestrator) upsertFromProbe(id, host, addr string, port int, res *ProbeResult, dc *DockerContainer) {
	svc := registry.Service{
		ID:       id,
		Host:     host,
		Address:  addr,
		Port:     port,
		Scheme:   res.Scheme,
		URL:      fmt.Sprintf("%s://%s:%d/", res.Scheme, host, port),
		Title:    res.Title,
		Icon:     res.Icon,
		Source:   "tailscale",
		Category: host,
	}
	if dc != nil {
		svc.Source = "docker"
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
		URL:      fmt.Sprintf("%s://%s:%d/", es.scheme, es.host, es.port),
		Source:   "docker",
		Category: es.host,
	}
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
