# webport - Traefik Route Manager for Development Servers

webport publishes HTTPS routes for local development servers by writing
Traefik file-provider configuration. Routes can be registered through
`webport`, the REST API, or `webportctl`. The zero-configuration wrapper and
configured sessions discover listeners in the developer's process context;
legacy daemon-side process discovery is disabled by default. Manual routes
expire unless refreshed.

Route hostnames use:

```text
{project}-{branch-slug}.{base-domain}
```

For example, `myapp` plus `feature/auth` becomes
`myapp-feature-auth.dev.example.com`.

## Current Architecture

- `webport` is both the daemon and the user-facing CLI. The daemon owns the
  in-memory route store, REST API, TTL checker, TLS setup, and Traefik dynamic
  configuration. Legacy daemon-side process discovery is an opt-in migration
  path.
- `webport dev` owns the foreground wrapper or configured multi-service
  session, including process supervision, readiness, named ports, route leases,
  logs, exports, and authenticated session control.
- Traefik watches the generated YAML through its file provider. webport
  atomically replaces the file; it does not invoke a reload command.
- `webportctl` registers a manual route, sends heartbeats, and unregisters it
  on shutdown. It can infer project and branch from Git.
- `webport-dns` inspects or synchronizes persistent wildcard A/AAAA records for
  Cloudflare, DigitalOcean, and Route53.
- TLS is either public ACME/DNS-01 through Traefik or a private local CA managed
  by webport.
- Linux uses systemd (`webport-stack.target`, `webport.service`, and
  `traefik.service`). macOS uses native LaunchDaemons.

The service is intentionally stateless across restarts. Graceful shutdown stops
discovery and certificate renewal, stops the TTL checker, clears the store,
publishes a route-free Traefik configuration, and shuts down the HTTP server.

## Repository Layout

```text
webport/
├── cmd/
│   ├── webport/                 # Daemon and unified CLI
│   ├── webportctl/              # Route lifecycle client, Git detection
│   └── webport-dns/             # Wildcard DNS status/sync CLI
├── internal/
│   ├── api/                     # REST handlers
│   ├── config/                  # Daemon environment configuration
│   ├── discovery/               # Linux/macOS legacy daemon discovery
│   ├── devsession/              # Configured foreground session runtime
│   ├── dnsmanager/              # Provider-independent wildcard DNS logic
│   ├── localca/                 # Private CA and wildcard leaf lifecycle
│   ├── route/                   # Route types, domains, store, and TTL checker
│   ├── shutdown/                # Signal-driven graceful cleanup
│   └── traefik/                 # Dynamic YAML generation and atomic writer
├── macos/                       # LaunchDaemon templates and runner scripts
├── scripts/                     # Installers, helpers, and integration tests
├── systemd/                     # Linux units and environment example
├── traefik/                     # Managed Traefik configuration template
├── mise.toml                    # Tool pin and build/test/install tasks
├── README.md                    # User and operator documentation
└── verify.md                    # Manual verification checklist
```

## HTTP API

| Method | Path                                    | Purpose |
| --- | --- | --- |
| `POST` | `/routes` | Register or replace a manual route |
| `GET` | `/routes` | List active manual and discovered routes |
| `DELETE` | `/routes/{project}:{branch}` | Delete a route |
| `POST` | `/routes/{project}:{branch}/heartbeat` | Refresh a manual route TTL |
| `GET` | `/config` | Return base domain, TTL, TLS mode, and optional CA path |
| `GET` | `/health` | Return `OK` |
| `GET` | `/ready` | Return publication readiness |
| `GET` | `/status` | Return daemon publication and lease status |

The preferred lease API is `POST /v1/leases`,
`POST /v1/leases/{lease_id}/heartbeat`, and
`DELETE /v1/leases/{lease_id}`. The `/routes` endpoints remain for legacy
clients.

Registration body:

```json
{
  "project": "myapp",
  "branch": "feature/auth",
  "port": 3000,
  "ttl": 600
}
```

Route identities use the unambiguous `{project}:{branch}` format, not the
hostname slug. URL-escape the complete ID when it is a path segment, especially
branch slashes:

```text
DELETE /routes/myapp:feature%2Fauth
```

Project components accept alphanumerics, dashes, and underscores. Branches
accept the same characters plus slash-separated components. Hostname generation
replaces branch slashes with dashes.

## Route Ownership and Concurrency

- `route.Store` protects its map with `sync.RWMutex`; `List` returns a snapshot.
- Manual routes have source `manual` and are removed by the TTL checker.
- Legacy daemon-discovered routes have source `process`, do not use TTL
  expiration, and are atomically reconciled from process scans. Configured
  session routes use renewable leases and are activated after readiness.
- A manual route wins over a discovered route with the same identity.
- Ambiguous discovery claims fail closed. A vanished discovered route is
  retained for one scan and removed after the second consecutive miss.
- All producers publish through one mutex-serialized callback so concurrent
  API, TTL, and discovery updates cannot interleave file writes.

## Process Discovery

The preferred discovery path is the `webport dev` wrapper or a configured
session. Legacy daemon-side discovery is supported on Linux and macOS but is
disabled by default. A legacy process opts in with an unambiguous route
identity:

```bash
WEBPORT_ROUTE='myapp:feature/auth' npm run dev
```

The daemon finds TCP listeners owned by that process and probes for HTTP. If
more than one listener is suitable, select the owned port explicitly:

```bash
WEBPORT_ROUTE='myapp:feature/auth' WEBPORT_APP_PORT=5173 npm run dev
```

Linux reads `/proc`; macOS uses `kern.procargs2` and machine-readable `lsof`
output. Containers and PID namespaces may require `webportctl` instead.

## Traefik and TLS

`internal/traefik.GenerateDynamicConfig` emits the complete managed document:
routers and services for the current snapshot plus a default wildcard
certificate definition. Route output is sorted for deterministic files.
Backends default to `127.0.0.1`; discovery may provide another loopback-reachable
host.

`internal/traefik.FileWriter` creates the destination directory and uses a
same-directory temporary file, `Sync`, and `Rename` for atomic replacement.
Traefik detects the change through its watched file provider.

In `acme` mode, the generated TLS store references Traefik's configured
certificate resolver. In `local-ca` mode, webport creates a domain-constrained
ECDSA CA and wildcard certificate in `WEBPORT_LOCAL_CA_DIR`. The CA lasts ten
years; the 90-day leaf is checked twice daily and renewed before expiration.
Never expose or trust `ca.key`; clients trust only `ca.crt`.

## Daemon Configuration

| Variable | Default | Description |
| --- | --- | --- |
| `WEBPORT_BASE_DOMAIN` | required | Base domain for generated hostnames |
| `WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH` | `/etc/traefik/dynamic/webport.yml` | Generated file-provider YAML |
| `WEBPORT_TRAEFIK_ENTRYPOINT` | `websecure` | Traefik HTTPS entrypoint |
| `WEBPORT_TRAEFIK_CERT_RESOLVER` | `webport` | ACME resolver |
| `WEBPORT_TLS_MODE` | `acme` | `acme` or `local-ca` |
| `WEBPORT_LOCAL_CA_DIR` | `webport-pki` beside generated YAML | Local CA state |
| `WEBPORT_LISTEN_HOST` | `127.0.0.1` | REST listen host |
| `WEBPORT_PORT` | `8080` | REST listen port |
| `WEBPORT_DEFAULT_TTL` | `30s` | Default manual-route lifetime |
| `WEBPORT_TTL_CHECK_INTERVAL` | `10s` | Expiration scan interval |
| `WEBPORT_SHUTDOWN_TIMEOUT` | `5s` | HTTP shutdown timeout |
| `WEBPORT_DISCOVERY_ENABLED` | `false` | Enable legacy daemon process discovery |
| `WEBPORT_DISCOVERY_INTERVAL` | `2s` | Discovery reconciliation interval |

`webport-dns` additionally reads `WEBPORT_DNS_PROVIDER`,
`WEBPORT_DNS_CREDENTIALS_FILE`, and optional `WEBPORT_DNS_ZONE`. Provider
credentials belong in a mode-`0600` environment file, never command-line
arguments.

Caddy-era `WEBPORT_CADDY*` and `WEBPORT_TLS_DNS*` variables are obsolete and
must not be reintroduced.

## Build, Test, and Development

```bash
mise run all                    # Build all three host binaries
mise run build                  # Build only webport
mise run build-client           # Build webportctl
mise run build-dns              # Build webport-dns
mise run test                   # go test -v ./...
mise run lint                   # go vet ./...
mise run fmt                    # go fmt ./...
mise run dev                    # Write /tmp/webport-traefik.yml
mise run build-all              # Linux/macOS, amd64/arm64 release binaries
mise run installer-test         # Linux installer test suite
mise run installer-test-macos   # macOS installer test suite
```

Direct Go equivalents are acceptable, especially for a focused package:

```bash
go test ./...
go test ./internal/route
go test ./internal/discovery -run TestName -count=1
```

The live macOS discovery test is opt-in:

```bash
WEBPORT_RUN_LIVE_DISCOVERY_TEST=1 go test ./internal/discovery \
  -run TestLiveProcessDiscovery -count=1 -v
```

Prefer focused unit tests while iterating, then run `mise run test`. Changes to
installers, service templates, or platform behavior should also run the
relevant installer test. Do not run installation tasks as root; the tasks
perform their own narrowly scoped elevation.

## Contributor Notes

- Keep platform-specific discovery behind the existing build-tagged files.
- Preserve deterministic Traefik output and atomic publication.
- Treat route IDs and hostname slugs as different concepts; never recover an
  identity by parsing a generated hostname.
- Preserve manual-route precedence and fail-closed discovery conflicts.
- Avoid logging provider secrets or accepting them as CLI values.
- Tests use small interfaces/fakes such as `traefik.Writer`; no live Traefik,
  DNS provider, systemd, or LaunchDaemon should be needed for unit tests.
- The only non-standard runtime dependencies are go-systemd, libdns provider
  packages, and `golang.org/x/sys`; proxying itself is handled by the external
  official Traefik binary.
