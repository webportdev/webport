# webport - Caddy Config Manager for Dev Servers

A systemd service that dynamically generates and manages Caddy reverse proxy configurations for localhost development servers, providing publicly accessible HTTPS URLs with automatic cleanup.

## Project Overview

webport provides a simple HTTP REST API that dev servers can use to register routes. It generates a Caddyfile and reloads Caddy automatically. Routes have a TTL (time-to-live) and expire after inactivity unless refreshed via heartbeat.

**Route naming format:** `{projectName}-{gitBranch}.{baseDomain}`

- Example: `myapp-feature-auth.mond.ooo` → localhost:3000

## Architecture

- **HTTP REST API** on localhost (default `127.0.0.1:8080`)
- **Thread-safe route storage** using `sync.RWMutex`
- **TTL checker goroutine** for automatic cleanup
- **Graceful shutdown** - clears all routes on exit
- **Systemd integration** - `Type=notify` with watchdog support

## Project Structure

```
webport/
├── cmd/webport/main.go              # Entry point, systemd notify/watchdog
├── cmd/webport-dns/main.go          # Wildcard DNS status/sync administration CLI
├── internal/
│   ├── config/config.go             # Environment variable configuration
│   ├── route/
│   │   ├── types.go                 # Route, RouteID, request/response structs
│   │   ├── store.go                 # Thread-safe route storage (sync.RWMutex)
│   │   └── ttl.go                   # Goroutine-based TTL checker
│   ├── caddy/
│   │   ├── generator.go             # Caddyfile template generation
│   │   └── reload.go                # Atomic write + caddy reload execution
│   ├── api/handlers.go              # HTTP REST handlers
│   └── shutdown/graceful.go         # Signal handling, cleanup on exit
├── systemd/webport.service          # systemd unit file
├── mise.toml                        # Go tool pinning and task definitions
├── README.md                        # Full documentation
└── verify.md                        # Testing checklist
```

## HTTP API Endpoints

| Method | Path                                   | Description                                           |
| ------ | -------------------------------------- | ----------------------------------------------------- |
| POST   | `/routes`                              | Register/update route with project, branch, port, ttl |
| GET    | `/routes`                              | List all active routes                                |
| DELETE | `/routes/{project}-{branch}`           | Remove a route                                        |
| POST   | `/routes/{project}-{branch}/heartbeat` | Refresh TTL for a route                               |
| GET    | `/health`                              | Health check for systemd                              |

**Request body for POST /routes:**

```json
{
  "project": "myapp",
  "branch": "feature/auth",
  "port": 3000,
  "ttl": 600
}
```

## Environment Variables

| Variable                     | Default                                      | Description                    |
| ---------------------------- | -------------------------------------------- | ------------------------------ |
| `WEBPORT_BASE_DOMAIN`        | _required_                                   | Base domain (e.g., "mond.boo") |
| `WEBPORT_CADDYFILE_PATH`     | `/etc/caddy/conf.d/Caddyfile`                | Output Caddyfile path          |
| `WEBPORT_LISTEN_HOST`        | `127.0.0.1`                                  | HTTP API listen host           |
| `WEBPORT_PORT`               | `8080`                                       | HTTP API port                  |
| `WEBPORT_DEFAULT_TTL`        | `300s`                                       | Route expiration time          |
| `WEBPORT_CADDY_RELOAD_CMD`   | `caddy reload --config /etc/caddy/Caddyfile` | Reload command                 |
| `WEBPORT_TTL_CHECK_INTERVAL` | `30s`                                        | Cleanup check frequency        |

## Key Implementation Details

### Route ID Parsing

- Route IDs are formatted as `{project}-{branch}` where branch slashes are replaced with dashes
- Parsing splits on the **last dash** to separate project from branch
- Example: `myapp-feature-auth` → project=`myapp-feature`, branch=`auth`
- This means project names with dashes are supported, but branch names with slashes create ambiguity

### Thread Safety

- `route.Store` uses `sync.RWMutex` to protect route map
- All operations (Add, Get, Delete, List) are properly locked
- TTL checker runs in separate goroutine with callback for expirations

### Atomic Caddyfile Updates

- Write to `.tmp` file in same directory
- Use `rename()` for atomic swap (POSIX guarantees)
- Then execute `caddy reload --config` command

### Graceful Shutdown

- On SIGTERM/SIGINT: stop TTL checker, clear all routes, write empty Caddyfile, shutdown HTTP server
- Notifies systemd of stopping state
- All cleanup happens before exit

## Build and Development

```bash
# Build
mise run build

# Run tests
mise run test

# Run in development mode (doesn't modify system Caddy)
mise run dev

# Install to system
mise run install
mise run install-service

# View logs
sudo journalctl -u webport -f
```

## Design Decisions

1. **HTTP REST over Unix Socket** - Simpler debugging, language-agnostic, easier testing
2. **Combined cleanup strategy** - Heartbeat (TTL) + graceful DELETE on shutdown
3. **Configurable paths via env vars** - Flexibility for different deployments
4. **Auto-reload Caddy** - Service handles reload, not external process
5. **Systemd Type=notify** - Proper service readiness signaling

## Dependencies

- `github.com/coreos/go-systemd/v22` - Systemd integration (sdnotify, watchdog)
- Standard library only for everything else

## Testing

- Unit tests for all packages
- Mock `caddy.Writer` interface for testing without actual Caddy
- Run `mise run test` or `go test ./...`
- See `verify.md` for comprehensive testing checklist
