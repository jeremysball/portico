// Package web serves the dashboard UI and its small JSON/SSE API.
package web

import (
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"html/template"
	"io/fs"
	"log/slog"
	"net/http"
	"time"

	"github.com/jeremysball/portico/internal/registry"
)

//go:embed templates/*.html
var templatesFS embed.FS

//go:embed static/*
var staticFS embed.FS

// hostSweeper is the subset of discovery.Orchestrator the web layer needs to
// trigger an on-demand full-port sweep of one tailnet host.
type hostSweeper interface {
	SweepHost(ctx context.Context, host string) error
}

type Server struct {
	reg     *registry.Registry
	log     *slog.Logger
	tmpl    *template.Template
	title   string
	sweeper hostSweeper
}

func New(reg *registry.Registry, log *slog.Logger, siteTitle string, sweeper hostSweeper) *Server {
	tmpl := template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	return &Server{reg: reg, log: log, tmpl: tmpl, title: siteTitle, sweeper: sweeper}
}

func (s *Server) Handler() http.Handler {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("GET /sw.js", s.handleServiceWorker)
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/services", s.handleList)
	mux.HandleFunc("PATCH /api/services/{id}", s.handleUpdate)
	mux.HandleFunc("DELETE /api/services/{id}", s.handleDelete)
	mux.HandleFunc("POST /api/hosts/{host}/refresh", s.handleRefreshHost)
	mux.HandleFunc("GET /events", s.handleEvents)
	return mux
}

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	data := struct{ Title string }{Title: s.title}
	if err := s.tmpl.ExecuteTemplate(w, "index.html", data); err != nil {
		s.log.Error("render index", "err", err)
	}
}

// handleServiceWorker serves sw.js from root scope (rather than /static/)
// so its default registration scope covers the whole app, not just /static/.
func (s *Server) handleServiceWorker(w http.ResponseWriter, r *http.Request) {
	b, err := staticFS.ReadFile("static/sw.js")
	if err != nil {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "application/javascript; charset=utf-8")
	w.Header().Set("Cache-Control", "no-cache")
	w.Write(b)
}

func (s *Server) handleList(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, s.reg.List())
}

type updateRequest struct {
	Name     *string `json:"name"`
	Category *string `json:"category"`
	Hidden   *bool   `json:"hidden"`
}

func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	var req updateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid body", http.StatusBadRequest)
		return
	}
	if !s.reg.Update(id, req.Name, req.Category, req.Hidden) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleDelete(w http.ResponseWriter, r *http.Request) {
	s.reg.Delete(r.PathValue("id"))
	w.WriteHeader(http.StatusNoContent)
}

// handleRefreshHost triggers an immediate full-port sweep of a single
// tailnet host, for a user who knows a service just came up there and
// doesn't want to wait for the next scheduled sweep. Docker-sourced hosts
// have nothing to sweep (Docker discovery already runs every few seconds),
// so this always targets tailnet.
func (s *Server) handleRefreshHost(w http.ResponseWriter, r *http.Request) {
	host := r.PathValue("host")
	if err := s.sweeper.SweepHost(r.Context(), host); err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	ch, cancel := s.reg.Subscribe()
	defer cancel()

	fmt.Fprintf(w, "event: update\ndata: {}\n\n")
	flusher.Flush()

	keepalive := time.NewTicker(15 * time.Second)
	defer keepalive.Stop()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ch:
			fmt.Fprintf(w, "event: update\ndata: {}\n\n")
			flusher.Flush()
		case <-keepalive.C:
			fmt.Fprintf(w, ": keepalive\n\n")
			flusher.Flush()
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	if err := json.NewEncoder(w).Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
