# Portico

[![CI](https://github.com/jeremysball/portico/actions/workflows/ci.yml/badge.svg)](https://github.com/jeremysball/portico/actions/workflows/ci.yml)
[![License: MIT](https://img.shields.io/badge/license-MIT-blue.svg)](LICENSE)

A small, self-updating landing page for your self-hosted services. No YAML
to hand-maintain — point it at your Tailscale socket and Docker socket and
it finds things on its own:

- **Local Docker containers** — enumerated via the Docker Engine API, using
  published ports and (optional) `portico.*` labels.
- **Other machines on your tailnet** — enumerated via the Tailscale local
  API (`tailscaled`'s socket), then lightly port-probed for HTTP(S).

New services typically show up within `DISCOVERY_INTERVAL` (default 5s) of
coming online, pushed to the browser live over SSE — no refresh needed.

## Quickstart

```sh
curl -O https://raw.githubusercontent.com/jeremysball/portico/main/docker-compose.yml
docker compose up -d
```

This pulls the published image (`ghcr.io/jeremysball/portico:latest`) rather
than building from source. To build locally instead, clone the repo and
uncomment `build: .` in `docker-compose.yml` (or run `docker compose up -d
--build`).

Then open `http://<this-host>:8080`.

The compose file uses `network_mode: host` so the container can reach both
sibling containers' published ports (via `localhost`) and tailnet peers'
`100.x.x.x` addresses directly. **This means the container shares your
host's full network namespace — only run trusted code here.** If that's not
acceptable for your setup, drop `network_mode: host` and instead: (a) attach
the container to the same Docker network(s) as the services you want it to
see, and (b) run it on a machine that already has a tailnet route (e.g. the
host itself, or give the container `NET_ADMIN` and run `tailscaled` inside
it directly).

It also mounts, read-only:
- `/var/run/docker.sock` — to list containers on this host.
- `/var/run/tailscale/tailscaled.sock` — to list tailnet peers. This assumes
  `tailscaled` is already running on the **host** (not in this container).
  If your tailscaled socket lives elsewhere, adjust the volume mount and
  `TAILSCALE_SOCKET`.

Either mount is optional — if a socket isn't present, that discovery source
is silently skipped.

> **Note:** this repo (and therefore its GHCR package) is currently
> **private**. The published-image pull above will only work for you and
> anyone else with repo access until it's made public. Flipping the repo to
> public does *not* automatically make the package public — that's a
> separate toggle under the package's own Settings → Danger Zone → Change
> visibility on GHCR.

## Labeling Docker containers

By default, any running container with a published TCP port is probed and
shown automatically. Use labels to fine-tune:

| Label | Effect |
|---|---|
| `portico.enable=false` | Exclude this container entirely |
| `portico.name=Jellyfin` | Override the displayed name |
| `portico.icon=<url>` | Override the tile icon |
| `portico.category=Media` | Group under a custom category instead of the hostname |
| `portico.url=https://host:port` | Register this exact URL directly, skipping port probing (useful for services behind a reverse proxy) |

```yaml
services:
  jellyfin:
    image: jellyfin/jellyfin
    labels:
      - portico.name=Jellyfin
      - portico.category=Media
      - portico.icon=https://cdn.jsdelivr.net/gh/selfhst/icons/svg/jellyfin.svg
```

## Customizing in the UI

Hover a tile and click the `⋯` button to rename it, change its category, or
hide it. Customizations persist in `/data/state.json` across restarts and
across rediscovery — they're layered on top of whatever's auto-detected.

## Configuration

All via environment variables, all optional:

| Variable | Default | Meaning |
|---|---|---|
| `PORT` | `8080` | HTTP listen port |
| `SITE_TITLE` | `Home` | Page title / heading |
| `DATA_DIR` | `/data` | Where `state.json` is persisted |
| `DISCOVERY_INTERVAL` | `5s` | How often to re-scan |
| `PROBE_TIMEOUT` | `1500ms` | Per-request HTTP timeout when probing a port |
| `PROBE_CONCURRENCY` | `40` | Max concurrent in-flight probes |
| `PORTS` | see `cmd/portico/main.go` | Comma-separated list of ports to probe on every tailnet host |
| `DOCKER_SOCKET` | `/var/run/docker.sock` | Docker Engine API socket |
| `TAILSCALE_SOCKET` | `/var/run/tailscale/tailscaled.sock` | tailscaled local API socket |
| `SSH_ENABLED` | `false` | SSH into online tailnet peers to discover listening ports and Docker containers, in addition to port-probing |
| `SSH_USER` | `root` | SSH username for tailnet connections; set to a non-root account if your Tailscale SSH policy requires one |
| `SSH_INTERVAL` | `5m` | How often to run the SSH discovery pass |
| `SSH_TIMEOUT` | `15s` | Per-host dial + handshake + command timeout |
| `SSH_CONCURRENCY` | `3` | Max hosts SSH'd into concurrently |

## How it decides what's "online"

Every discovery pass, each target (tailnet-host × port, or docker
container's published port) gets a short HTTP(S) request. Any response at
all — including 401/403/redirects — counts as "up" (plenty of dashboards
require auth on `/`). Anything not seen in the latest pass is marked
offline but stays in the list (dimmed) rather than disappearing, so a
flaky service doesn't flicker in and out of the page.

## Development

```sh
go build ./...
go vet ./...
go run ./cmd/portico
```

## License

[MIT](LICENSE)
