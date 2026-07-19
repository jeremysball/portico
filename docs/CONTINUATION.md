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
   30s. Motivated by a confirmed live investigation — see point 6.

3. **Fixed junk titles.** `<title>` is only scraped on 2xx responses now, so
   401/403/404 pages can't leak "Unauthorized"/"Not Found" in as a fake
   service name (`internal/discovery/probe.go`).

4. **Untitled services now show "Not Found"** instead of literally repeating
   `host:port` as both the name and the subtitle on the same tile.

5. **Added a bounded (7-day, 500-entry-cap) online/offline history log** per
   service, viewable via a new 🕓 button on each tile. Rewrote the frontend
   to patch tiles in place instead of tearing down and rebuilding the whole
   DOM on every SSE update (fixes flicker/reordering). Gave the
   online-status dot a pulsing glow + radiating ping-ring animation.

6. **Investigated whether Portico caused tailnet reliability issues — yes,
   confirmed live.** Full writeup: `docs/tailnet-investigation-2026-07-19.md`.
   The old 5s tailnet-wide port scan was producing ~70 RST/sec sustained
   against tailnet peers, captured directly from `journalctl -u tailscaled`
   while the old container was still running. Destination host/port
   fingerprint matched Portico's port list and discovered peers exactly.

7. **Rebranded**: new logo/icon (`internal/web/static/logo.svg`, `icon.svg`,
   rendered PNGs at 16/32/180/192/512) in a foggy-night, Innsmouth/Lovecraft
   aesthetic. Retheme applied across `style.css`. Converted the app into an
   installable PWA: `manifest.json`, `sw.js` (caches the static shell only —
   `/api/*` and `/events` always hit the network), registered from `app.js`,
   served at `/sw.js` (not `/static/sw.js`) so its scope covers the whole app.

8. **Fixed duplicate http/https tiles for the same app.** Two causes, both
   addressed: (a) `probe.go` now treats a response that redirects to the
   same host on a *different* port as a stub, not a distinct service (the
   common `:80 -> :443` pattern) — a redirect to a different *hostname* is
   left alone, since that could be a legitimate canonical-domain redirect
   rather than a same-box stub; (b) `discovery.go` now buffers each pass's
   results and collapses same-host entries sharing an identical scraped
   title before upserting (catches dual listeners that don't redirect at
   all), preferring https then the lower port.

9. **Added opportunistic nmap-based service/version identification.** Not
   part of the hot polling path — a new, separate, deliberately rare ticker
   (`IDENTIFY_INTERVAL`, default 6h) runs a single-port, low-intensity
   `nmap -sV -Pn` against services *already confirmed online* by the
   existing HTTP probes (never a port scan itself), and records what it
   finds (e.g. "nginx 1.24.0") as a small subtitle on the tile. No-ops
   cleanly via `exec.LookPath` if nmap isn't in the image. Added `nmap` to
   the Dockerfile. XML parsing is unit-tested against a fixture since nmap
   itself isn't installed in this sandbox to test end-to-end.

10. **Reconciled a long-open PR.** GitHub PR #1
    (`feat/watchtower-auto-deploy`, opened 2026-07-13, never merged) added a
    scoped Watchtower service (auto-pulls `:latest`, restarts `portico`) and
    fixed a real bug in `release.yml`: the old trigger tagged branch pushes
    as `:main` only — `latest=auto` fires solely on semver tags — so merges
    to main were never actually updating `:latest`, the tag
    `docker-compose.yml` pulls and Watchtower follows. That means **this
    session's earlier pushes never actually reached the running container**
    despite succeeding. Merged via `git merge` (not cherry-pick, so GitHub
    correctly auto-detected and closed the PR as merged) at commit
    `c5d80f2`. `release.yml` now publishes `:latest` + `:sha-<short>` on
    merge to main, gated on the `CI` workflow completing successfully first
    (was racing CI before).

All of the above builds clean (`go build ./...`, `go vet ./...`,
`gofmt -l .`, `go test ./...` all pass) and is on `origin/main`.

## Not yet done / needs your attention

- **Confirm the auto-deploy pipeline actually works end to end.** This is
  the first push that should exercise the full chain: merge to main → CI
  passes → Release publishes `:latest` + `:sha-<short>` → Watchtower
  (60s poll interval) notices and restarts `portico`. Check:
  ```sh
  docker ps --format '{{.Names}}\t{{.Image}}\t{{.Status}}' | grep -E 'portico|watchtower'
  docker inspect portico --format '{{.Image}}'  # should change after Watchtower restarts it
  docker logs watchtower --since 10m
  ```
  If Watchtower isn't picking it up, `docker compose up -d` once manually
  to get both `portico` and the new `watchtower` service running (the
  Watchtower service is new to `docker-compose.yml` as of this merge, so it
  needs an initial `up`, not just a pull).
- **After it's actually deployed, re-run the tailnet measurement** to
  confirm the fix worked (real before/after, not just theory):
  ```sh
  journalctl -u tailscaled --since "-10min" | grep -c open-conn-track
  ```
  Pre-fix baseline captured this session: 508 in 10 minutes (~43,884
  individual RST events accounting for the rate-limiter's suppressed
  counts). Should drop substantially.
- **Biggest remaining lever if the storm is still too noisy after
  deploy:** trim the `PORTS` env var in `docker-compose.yml` down to only
  the ports you actually run services on, instead of the full 32-port
  default list.
- **One-time customization reset:** Docker-sourced service IDs changed
  format (namespaced with `docker:` prefix). Name/category overrides
  previously set on *Docker-hosted* tiles will reset once on first restart.
  Tailnet-hosted tiles are unaffected.
- **Visual QA not done.** No headless browser in this sandbox
  (`chromium-cli` unavailable) — the new theme, fog animation, PWA install
  prompt, dedup behavior, and detected-service subtitle were verified by
  code review, unit tests, and a local `go run` smoke test hitting the API
  directly, not by looking at the rendered page in a real browser.
- **nmap identification is unverified end-to-end.** The XML parser is unit
  tested, but the actual `exec.Command("nmap", ...)` path has never run
  against a real nmap binary in this sandbox (not installed here). Worth
  watching the first `IDENTIFY_INTERVAL` cycle after deploy (fires ~2min
  after container start, then every 6h) to confirm it populates `Detected`
  correctly on real services and doesn't error/hang.
- **Unconfirmed thread from the investigation:** a handful of connection
  timeouts hit non-tailnet public IPs (Hetzner ranges) tagged "no
  associated peer node" in the tailscaled logs. Not explained by Portico's
  own probing logic. Flagged as open, not investigated further.
- **Monitoring recommendations from the investigation doc are written up
  but not implemented** — cheapest option is a `journalctl` grep + alert
  cron; more useful is scraping `tailscale debug metrics` (already exposes
  drop/error counters like `tstun_out_to_wg_drop` in Prometheus format, zero
  extra exporters needed) with a real Prometheus/Grafana pair. Full detail
  in the investigation doc.
- **Housekeeping offered, not done:** the merged PR branch
  `feat/watchtower-auto-deploy` still exists on GitHub and could be deleted
  (`gh api -X DELETE repos/jeremysball/portico/git/refs/heads/feat/watchtower-auto-deploy`
  or the "Delete branch" button on the PR page) — didn't do this
  unprompted since it's a repo-state change beyond what was asked.

## Key file map

- `internal/discovery/discovery.go` — orchestrator: 3 tickers (Docker,
  tailnet, identify), dedup logic
- `internal/discovery/probe.go` — HTTP probing, title scraping, redirect-stub
  detection
- `internal/discovery/identify.go` — nmap service/version lookup + XML parsing
- `internal/registry/registry.go` — service store, history log, Detected field
- `internal/web/static/{app.js,style.css,sw.js,manifest.json}` — frontend
- `internal/web/static/{logo.svg,icon.svg,icon-*.png}` — branding
- `docs/tailnet-investigation-2026-07-19.md` — full investigation writeup
- `docker-compose.yml` — deployment config: `PORT=8888` (pre-existing),
  `TAILNET_INTERVAL=30s`, `IDENTIFY_INTERVAL=6h` added this session; now
  also carries the merged-in Watchtower service + `pull_policy`/labels
- `.github/workflows/release.yml` — now gated on CI passing, publishes
  `:latest` + `:sha-<short>` on merge to main
