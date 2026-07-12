// Package registry holds the current set of discovered services plus any
// user customizations, and persists both across restarts.
package registry

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

// Service is one discovered (or user-added) endpoint.
type Service struct {
	ID        string    `json:"id"`
	Host      string    `json:"host"`    // tailnet hostname, e.g. "myhost"
	Address   string    `json:"address"` // IP the port was reached on
	Port      int       `json:"port"`
	Scheme    string    `json:"scheme"` // "http" or "https"
	URL       string    `json:"url"`
	Title     string    `json:"title"` // scraped <title> or docker label
	Icon      string    `json:"icon"`
	Source    string    `json:"source"` // "docker", "tailscale", "probe"
	Online    bool      `json:"online"`
	FirstSeen time.Time `json:"firstSeen"`
	LastSeen  time.Time `json:"lastSeen"`

	// User customizations, preserved across rediscovery.
	NameOverride     string `json:"nameOverride,omitempty"`
	CategoryOverride string `json:"categoryOverride,omitempty"`
	Category         string `json:"category"` // effective category (source-derived default)
	Hidden           bool   `json:"hidden,omitempty"`
}

// DisplayName returns the user override if set, else the scraped title, else host:port.
func (s Service) DisplayName() string {
	if s.NameOverride != "" {
		return s.NameOverride
	}
	if s.Title != "" {
		return s.Title
	}
	return fmt.Sprintf("%s:%d", s.Host, s.Port)
}

// EffectiveCategory returns the user override if set, else the default category.
func (s Service) EffectiveCategory() string {
	if s.CategoryOverride != "" {
		return s.CategoryOverride
	}
	return s.Category
}

// Registry is a thread-safe store of services, persisted to a JSON file.
type Registry struct {
	mu       sync.RWMutex
	services map[string]Service
	path     string

	subMu sync.Mutex
	subs  map[chan struct{}]struct{}
}

func New(dataDir string) *Registry {
	r := &Registry{
		services: make(map[string]Service),
		path:     filepath.Join(dataDir, "state.json"),
		subs:     make(map[chan struct{}]struct{}),
	}
	r.load()
	return r
}

func (r *Registry) load() {
	b, err := os.ReadFile(r.path)
	if err != nil {
		return
	}
	var svcs []Service
	if err := json.Unmarshal(b, &svcs); err != nil {
		return
	}
	r.mu.Lock()
	for _, s := range svcs {
		s.Online = false // will be re-marked online by the next discovery pass
		r.services[s.ID] = s
	}
	r.mu.Unlock()
}

func (r *Registry) save() {
	r.mu.RLock()
	svcs := make([]Service, 0, len(r.services))
	for _, s := range r.services {
		svcs = append(svcs, s)
	}
	r.mu.RUnlock()

	sort.Slice(svcs, func(i, j int) bool { return svcs[i].ID < svcs[j].ID })

	b, err := json.MarshalIndent(svcs, "", "  ")
	if err != nil {
		return
	}
	tmp := r.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o644); err != nil {
		return
	}
	os.Rename(tmp, r.path)
}

// Upsert merges a freshly-discovered service into the registry, preserving
// any existing user customizations for that ID.
func (r *Registry) Upsert(s Service) {
	now := time.Now()
	r.mu.Lock()
	if existing, ok := r.services[s.ID]; ok {
		s.FirstSeen = existing.FirstSeen
		s.NameOverride = existing.NameOverride
		s.CategoryOverride = existing.CategoryOverride
		s.Hidden = existing.Hidden
	} else {
		s.FirstSeen = now
	}
	s.LastSeen = now
	s.Online = true
	r.services[s.ID] = s
	r.mu.Unlock()

	r.save()
	r.notify()
}

// MarkOfflineExcept flips Online=false for any service not present in the
// given set of IDs seen during the most recent discovery pass.
func (r *Registry) MarkOfflineExcept(seen map[string]struct{}) {
	r.mu.Lock()
	changed := false
	for id, s := range r.services {
		if _, ok := seen[id]; !ok && s.Online {
			s.Online = false
			r.services[id] = s
			changed = true
		}
	}
	r.mu.Unlock()
	if changed {
		r.save()
		r.notify()
	}
}

func (r *Registry) List() []Service {
	r.mu.RLock()
	defer r.mu.RUnlock()
	svcs := make([]Service, 0, len(r.services))
	for _, s := range r.services {
		svcs = append(svcs, s)
	}
	sort.Slice(svcs, func(i, j int) bool {
		if svcs[i].EffectiveCategory() != svcs[j].EffectiveCategory() {
			return svcs[i].EffectiveCategory() < svcs[j].EffectiveCategory()
		}
		return svcs[i].DisplayName() < svcs[j].DisplayName()
	})
	return svcs
}

// Update applies a user customization (rename/recategorize/hide) to a service by ID.
func (r *Registry) Update(id string, name, category *string, hidden *bool) bool {
	r.mu.Lock()
	s, ok := r.services[id]
	if !ok {
		r.mu.Unlock()
		return false
	}
	if name != nil {
		s.NameOverride = *name
	}
	if category != nil {
		s.CategoryOverride = *category
	}
	if hidden != nil {
		s.Hidden = *hidden
	}
	r.services[id] = s
	r.mu.Unlock()

	r.save()
	r.notify()
	return true
}

func (r *Registry) Delete(id string) {
	r.mu.Lock()
	delete(r.services, id)
	r.mu.Unlock()
	r.save()
	r.notify()
}

// Subscribe returns a channel that receives a value whenever the registry changes.
// Call the returned function to unsubscribe.
func (r *Registry) Subscribe() (ch chan struct{}, cancel func()) {
	ch = make(chan struct{}, 1)
	r.subMu.Lock()
	r.subs[ch] = struct{}{}
	r.subMu.Unlock()
	return ch, func() {
		r.subMu.Lock()
		delete(r.subs, ch)
		close(ch)
		r.subMu.Unlock()
	}
}

func (r *Registry) notify() {
	r.subMu.Lock()
	defer r.subMu.Unlock()
	for ch := range r.subs {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}
