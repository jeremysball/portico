---
name: run
description: Use when launching Portico to verify a change, screenshot the dashboard, or confirm the app works outside Docker/docker-compose.
---

# Run Portico (no Docker)

`docker-compose.yml` runs Portico as a host-networked container with the
Docker and Tailscale sockets bind-mounted. For local dev/verification you
don't need any of that — it's a plain Go binary that degrades gracefully
when those sockets are missing.

## Launch

```bash
cd /workspace/portico   # or equivalent worktree path
go build -o /tmp/portico ./cmd/portico

mkdir -p /tmp/portico-data
DATA_DIR=/tmp/portico-data \
PORT=8888 \
DOCKER_SOCKET=/var/run/docker.sock \
TAILSCALE_SOCKET=/var/run/tailscale/tailscaled.sock \
/tmp/portico > /tmp/portico.log 2>&1 &
disown

sleep 2
curl -s -o /dev/null -w "%{http_code}\n" http://localhost:8888/
# expect 200
```

`go run ./cmd/portico` works too if you don't need a standalone binary.
Use a port other than `8888` if the live host container (see
`docker-compose.yml`) is also running on this box.

## What works without real sockets

- `DOCKER_SOCKET` pointing at a socket that doesn't exist: Docker discovery
  just finds nothing and logs an error — the server still starts and serves.
- `TAILSCALE_SOCKET` behaves the same way. If tailscaled *is* present on the
  host (common in this dev environment), Portico will actually discover and
  probe real tailnet peers — `/api/services` returns live results, not stubs.
- `DATA_DIR` must be a writable directory; the process creates it if missing.

## Verify it's actually running, not just listening

```bash
curl -s http://localhost:8888/api/services | head -c 500
```

A `200` on `/` only proves the HTML template rendered. Hitting
`/api/services` confirms the discovery orchestrator is actually populating
the registry (real entries with `host`/`address`/`online` fields, or `[]`
if no docker/tailscale sources are reachable — either is a legitimate
"working" result, just don't mistake an error page for an empty array).

## Kill the server

```bash
pkill -f '/tmp/portico$'
```
