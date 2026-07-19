# Continuation prompt — Portico session 2026-07-19

Paste this as the first message in a fresh session to pick up where this one
left off.

---

I'm continuing work on Portico (`~/portico`, self-hosted service dashboard,
Go + vanilla JS). In the last session we:

1. **Fixed the root causes of UI flicker / identity-swapping tiles.**
   Docker-sourced and tailnet-sourced service IDs used to collide on
   `host:port` when this machine's own tailnet peer entry overlapped with
   its own Docker containers (this box's tailnet hostname is `prometheus` —
   that's what "Prometheus 8000" actually was, not a real Prometheus
   server). Docker IDs are now prefixed `docker:` so they can never collide.
   See `internal/discovery/discovery.go`.

2. **Split discovery cadence.** Docker discovery (cheap, local) still runs
   every 5s (`DISCOVERY_INTERVAL`). Tailnet probing (expensive, cross-network
   — 32 ports x every peer) now runs on its own `TAILNET_INTERVAL`, default
   30s. This was motivated by a confirmed live investigation — see point 6.

3. **Fixed junk titles.** `<title>` is only scraped on 2xx responses now, so
   401/403/404 pages can't leak "Unauthorized"/"Not Found" in as a fake
   service name (`internal/discovery/probe.go`).

4. **Untitled services now show "Not Found"** instead of literally repeating
   `host:port` as both the name and the subtitle on the same tile.

5. **Added a bounded (7-day, 500-entry-cap) online/offline history log** per
   service, exposed via the existing `/api/services` response, viewable via
   a new 🕓 button on each tile. Rewrote the frontend to patch tiles in
   place instead of tearing down and rebuilding the whole DOM on every SSE
   update (fixes the flicker/reordering). Gave the online-status dot a
   pulsing glow + radiating ping-ring animation.

6. **Investigated whether Portico caused tailnet reliability issues — yes,
   confirmed live.** Full writeup: `docs/tailnet-investigation-2026-07-19.md`.
   Short version: the old 5s tailnet-wide port scan was producing ~70
   RST/sec sustained against tailnet peers, captured directly from
   `journalctl -u tailscaled` while the old container was still running.
   The destination host/port fingerprint matched Portico's port list and
   discovered peers exactly. The interval split in point 2 should cut this
   ~6x, but does not eliminate it — the port list itself is the next lever
   (see "Not yet done" below).

7. **Rebranded**: new logo/icon (`internal/web/static/logo.svg`,
   `icon.svg`, rendered PNGs at 16/32/180/192/512) in a foggy-night,
   Innsmouth/Lovecraft aesthetic — a classical portico glimpsed through
   drifting fog bands, phosphor-green glow, dark teal/near-black palette.
   Retheme applied across `style.css` (new CSS custom properties, animated
   fog drift on `body::before`, serif italic header). Converted the app
   into an installable PWA: `manifest.json`, `sw.js` (caches the static
   shell only — `/api/*` and `/events` are always network, never cached),
   registered from `app.js`, served at `/sw.js` (not `/static/sw.js`) so
   its scope covers the whole app.

All of the above builds clean (`go build ./...`, `go vet ./...`, `gofmt -l .`
all pass) and was committed to `origin/main`.

## Not yet done / needs your attention

- **The running production container has not been redeployed.** Pushing to
  `main` triggers `.github/workflows/release.yml`, which rebuilds and
  republishes `ghcr.io/jeremysball/portico:latest` — but the host still
  needs `docker compose pull && docker compose up -d` (or equivalent) to
  actually pick it up. Until that happens, the tailnet RST storm described
  in the investigation doc is **still occurring at the old 5-second, un-split
  cadence**, since the container currently running (as of this session) is
  the pre-fix build.
- **After redeploying, re-run the same measurement** to confirm the fix
  actually worked (validates the investigation's hypothesis with a real
  before/after, not just theory):
  ```sh
  journalctl -u tailscaled --since "-10min" | grep -c open-conn-track
  ```
  Pre-fix baseline captured this session: 508 in 10 minutes (~43,884
  individual RST events once you account for the rate-limiter's suppressed
  counts). Should drop substantially post-redeploy.
- **Biggest remaining lever if the storm is still too noisy after
  redeploy:** trim the `PORTS` env var in `docker-compose.yml` down to only
  the ports you actually run services on, instead of the full 32-port
  default list. Frequency reduction (done) helps; a shorter port list is a
  direct multiplicative reduction in connection attempts per pass.
- **One-time customization reset:** because Docker-sourced service IDs
  changed format (namespaced with `docker:` prefix to fix the collision
  bug), any name/category overrides you'd previously set via the ⋯ menu on
  *Docker-hosted* tiles will reset once on first restart after redeploy.
  Tailnet-hosted tiles are unaffected. You'll just need to re-set them once.
- **Visual QA not done.** This sandbox has no headless browser
  (`chromium-cli` unavailable), so the new theme, fog animation, PWA install
  prompt, and tile layout were verified by code review and rendered PNG
  previews of the logo/icon only — not by actually looking at the running
  page in a browser. Worth a real look before considering this finished.
- **Unconfirmed thread from the investigation:** a handful of connection
  timeouts hit non-tailnet public IPs (Hetzner ranges, `159.69.217.146:443`
  etc.) tagged "no associated peer node" in the tailscaled logs. Not
  explained by Portico's own probing logic (which only ever dials `100.x`
  tailnet IPs or `localhost`). Flagged as open, not investigated further.
- **Monitoring recommendations from the investigation doc are written up
  but not implemented** — cheapest option is a `journalctl` grep + alert
  cron; the more useful option is standing up a real Prometheus/Grafana
  pair (ironic given this host's tailnet name) and scraping `tailscale
  debug metrics`, which already exposes drop/error counters like
  `tstun_out_to_wg_drop` in Prometheus format with zero extra exporters
  needed. Full detail in the investigation doc.

## Key file map

- `internal/discovery/discovery.go` — orchestrator, now two tickers
- `internal/discovery/probe.go` — HTTP probing, title scraping
- `internal/registry/registry.go` — service store, history log
- `internal/web/static/{app.js,style.css,sw.js,manifest.json}` — frontend
- `internal/web/static/{logo.svg,icon.svg,icon-*.png}` — branding
- `docs/tailnet-investigation-2026-07-19.md` — full investigation writeup
- `docker-compose.yml` — deployment config (note: `PORT` was already
  locally changed to `8888` before this session; `TAILNET_INTERVAL=30s`
  added)
