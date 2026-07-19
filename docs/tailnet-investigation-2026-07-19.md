# Investigation: was Portico causing tailnet reliability issues?

**Date:** 2026-07-19
**Verdict: yes, with direct evidence.** The pre-fix discovery loop was generating
a connection-reset storm against tailnet peers on a 5-second cycle, confirmed
live on the `prometheus` node (this machine) while the old, unpatched
container was still running in production.

## Background

Portico's tailnet-hostname on this box is **`prometheus`** — this is *why*
the dashboard tile you were confused about ("what is Prometheus 8000?") was
never an actual Prometheus server. It was this machine's own tailnet peer
entry being scanned by its own discovery pass and colliding with a local
Docker container in the registry (see the ID-collision fix below). Mystery
solved as a side effect of the other bug fix already made this session.

## The mechanism

Before the fixes made earlier in this session, `internal/discovery/discovery.go`
ran a single 5-second ticker that, on every tick, probed **all 32 ports in
`defaultPorts` against every online tailnet host**, with up to 40 requests
in flight concurrently (`PROBE_CONCURRENCY`). Most of those ports aren't
listening on most hosts — that's expected, dashboards only occupy a handful
of them — so the overwhelming majority of every pass is failed connection
attempts: SYNs to closed ports, timeouts, resets.

## Live evidence gathered on this host

Captured directly from `journalctl -u tailscaled` and `tailscale debug
metrics` on 2026-07-19, ~15:27–15:38 local time, while the **old** (still
running, not-yet-redeployed) `portico` container was active:

- **508** `open-conn-track` timeout/RST log events in a 10-minute window
  (this is itself a rate-limited counter — tailscaled was suppressing most
  of the individual log lines).
- Summing the rate-limiter's own `(N dropped)` counts: **~43,884** individual
  "RST by peer" events in that same 10 minutes — roughly 70+ per second,
  sustained.
- The destination list is a smoking gun by itself. Every single timed-out
  connection target matches Portico's fingerprint exactly:
  - Destination ports: `443, 80, 5173, 9443, 8083, 8082, 8000, 8081, 9000,
    8181, 8096, 8080, 8001, 5000` — all from `defaultPorts`.
  - Destination hosts: `100.99.98.26` (sisyphus), `100.96.32.79` (iphone-15),
    `100.121.141.76` (iphone-15-mae) — all online tailnet peers Portico
    would have enumerated via `tailscale status`.
  - No other process on this host has a reason to open connections to this
    exact cross-product of hosts x ports on a fixed cadence.
- `tailscale debug metrics` showed elevated drop counters consistent with
  sustained connection churn: `tstun_out_to_wg_drop 6176`,
  `tstun_out_to_wg_drop_filter 416`, `magicsock_send_derp_dropped 35`.
- A handful of timeouts also hit **non-tailnet public IPs**
  (`159.69.217.146:443`, `95.216.195.133:80`, Hetzner ranges) tagged
  "no associated peer node." These are *not* explained by Portico's own
  probing logic (which only ever dials `100.x` tailnet IPs or `localhost`)
  and are more likely unrelated DERP/control-plane traffic or a stale
  `portico.url` label pointing at an external host — flagged here as an
  open, unconfirmed thread rather than asserted as caused by Portico.

## Why this plausibly reads as "tailnet reliability issues"

A sustained ~70 RST/sec background load, repeating every 5 seconds
indefinitely, is exactly the kind of noise that:
- Keeps `nf_conntrack` and tailscaled's own connection-tracking tables
  constantly churning instead of settling.
- Competes for the same NAT/firewall state and UPnP port-mapping churn on
  the LAN gateway (the `netcheck` output during this investigation shows
  active UPnP renegotiation traffic on this network).
- Produces exactly the symptom pattern of "vaguely flaky, hard to pin down,
  comes and goes" — because it's proportional to how many peers/ports exist,
  not tied to any one obviously-misbehaving device.

This isn't a proven root cause of every tailnet hiccup you've experienced,
but it is a confirmed, currently-active, self-inflicted source of
significant background network noise that had no good reason to exist at
that volume.

## What the earlier fix in this session already does about it

Already implemented and built (not yet deployed to the running container
as of this writing — see continuation prompt):
- Split Docker discovery (local, cheap) from tailnet discovery (cross-network,
  expensive) onto separate tickers. Tailnet probing now defaults to a
  30-second interval (`TAILNET_INTERVAL`) instead of 5 seconds — a 6x cut.
- This alone should cut the RST storm rate by roughly 6x. It does not
  eliminate it, since scanning 32 closed ports across N peers is inherently
  going to generate connection attempts to non-listening ports.

## Recommended follow-up (not yet done)

To actually shrink the *number* of ports probed per host (not just the
frequency), consider trimming `PORTS` to only the ports you actually run
services on, rather than the full 32-port default list. That's the biggest
remaining lever — frequency reduction helps, but a smaller port list per
pass is a direct multiplicative reduction in connection attempts.

## How to monitor for this going forward

Three options, cheapest first:

1. **`tailscale netcheck`** — run manually or on a cron when you suspect
   something's off. Shows DERP latency and whether you're getting direct
   connections vs relayed, which degrades under sustained congestion.

2. **`journalctl -u tailscaled` watch/alert** — the actual signal that
   caught this. A simple cron/systemd-timer script:
   ```sh
   journalctl -u tailscaled --since "-5min" | grep -c "open-conn-track" 
   ```
   Alert (e.g. via `ntfy.sh` webhook or email) if this count exceeds a
   threshold (a few hundred/5min would already be notable given the
   baseline captured above). This needs no new infrastructure.

3. **Scrape `tailscale debug metrics` with real Prometheus.** tailscaled
   already exposes a full Prometheus-format metrics dump locally — no
   separate exporter needed. Given this host is ironically *named*
   "prometheus" on the tailnet but doesn't actually run one, standing up
   a real Prometheus + Grafana pair (Portico would auto-discover them once
   they're running, at ports 9090/3000 which are already in the default
   port list) and scraping `tailscale debug metrics` on an interval would
   give you real dashboards and alerting on:
   - `tstun_out_to_wg_drop` / `tstun_out_to_wg_drop_filter` — packet drop
     counters, the clearest sustained-noise signal seen in this investigation.
   - `magicsock_send_derp_dropped`, `magicsock_num_derp_conns` — relay
     health.
   - `netcheck_report_error` — netcheck failures over time.

   This is the only option that gives you historical trend data instead of
   a point-in-time snapshot, which is what you'd actually want for "has
   this been happening for a while" questions like the one that started
   this investigation.
