// Package web serves the dashboard UI and its small JSON/SSE API.
package web

import (
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

type Server struct {
	reg   *registry.Registry
	log   *slog.Logger
	tmpl  *template.Template
	title string
}

func New(reg *registry.Registry, log *slog.Logger, siteTitle string) *Server {
	tmpl := template.Must(template.ParseFS(templatesFS, "templates/*.html"))
	return &Server{reg: reg, log: log, tmpl: tmpl, title: siteTitle}
}

func (s *Server) Handler() http.Handler {
	staticSub, err := fs.Sub(staticFS, "static")
	if err != nil {
		panic(err)
	}

	mux := http.NewServeMux()
	mux.Handle("GET /static/", http.StripPrefix("/static/", http.FileServer(http.FS(staticSub))))
	mux.HandleFunc("GET /{$}", s.handleIndex)
	mux.HandleFunc("GET /api/services", s.handleList)
	mux.HandleFunc("PATCH /api/services/{id}", s.handleUpdate)
	mux.HandleFunc("DELETE /api/services/{id}", s.handleDelete)
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
