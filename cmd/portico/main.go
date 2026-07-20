// Command portico is a self-updating dashboard for self-hosted services: it
// discovers Docker containers on its own host and HTTP(S) endpoints on other
// tailnet machines, and serves them as a live web UI.
package main

import (
	"context"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/jeremysball/portico/internal/discovery"
	"github.com/jeremysball/portico/internal/registry"
	"github.com/jeremysball/portico/internal/web"
)

// defaultPorts covers the ports of common self-hosted services (dashboards,
// media servers, *arr apps, home automation, etc). Override with PORTS.
var defaultPorts = []int{
	80, 443, 3000, 3001, 4000, 5000, 5001, 5173, 5678,
	7000, 7575, 8000, 8001, 8080, 8081, 8082, 8083, 8088, 8096,
	8123, 8181, 8443, 8888, 8989, 9000, 9090, 9091, 9092, 9096,
	9117, 9443, 32400,
}

func main() {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))

	dataDir := getEnv("DATA_DIR", "/data")
	if err := os.MkdirAll(dataDir, 0o755); err != nil {
		log.Error("create data dir", "err", err)
		os.Exit(1)
	}

	reg := registry.New(dataDir)

	discCfg := discovery.Config{
		Interval:             getEnvDuration("DISCOVERY_INTERVAL", 5*time.Second),
		TailnetInterval:      getEnvDuration("TAILNET_INTERVAL", 30*time.Second),
		TailnetSweepInterval: getEnvDuration("TAILNET_SWEEP_INTERVAL", 6*time.Hour),
		IdentifyInterval:     getEnvDuration("IDENTIFY_INTERVAL", 6*time.Hour),
		ProbeTimeout:         getEnvDuration("PROBE_TIMEOUT", 1500*time.Millisecond),
		Concurrency:          getEnvInt("PROBE_CONCURRENCY", 40),
		Ports:                getEnvPorts("PORTS", defaultPorts),
		DockerSocket:         getEnv("DOCKER_SOCKET", "/var/run/docker.sock"),
		TailscaleSocket:      getEnv("TAILSCALE_SOCKET", "/var/run/tailscale/tailscaled.sock"),
	}
	orch := discovery.NewOrchestrator(discCfg, reg, log)

	srv := web.New(reg, log, getEnv("SITE_TITLE", "Home"), orch)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go orch.Run(ctx)

	addr := ":" + getEnv("PORT", "8080")
	httpServer := &http.Server{Addr: addr, Handler: srv.Handler()}

	go func() {
		log.Info("listening", "addr", addr)
		if err := httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Error("http server", "err", err)
			os.Exit(1)
		}
	}()

	<-ctx.Done()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		log.Error("shutdown", "err", err)
	}
}

func getEnv(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func getEnvInt(key string, def int) int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func getEnvDuration(key string, def time.Duration) time.Duration {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func getEnvPorts(key string, def []int) []int {
	v := os.Getenv(key)
	if v == "" {
		return def
	}
	parts := strings.Split(v, ",")
	ports := make([]int, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		n, err := strconv.Atoi(p)
		if err != nil {
			continue
		}
		ports = append(ports, n)
	}
	if len(ports) == 0 {
		return def
	}
	return ports
}
