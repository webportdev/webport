# webport

webport gives local development servers stable HTTPS names:

```text
https://myapp-feature-auth.webport.localhost
                       ↓
             http://127.0.0.1:5173
```

It supervises renewable route leases and publishes deterministic Traefik
file-provider configuration. Traefik performs the actual HTTPS proxying.

## Quick start

Install as a regular user:

```bash
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/master/scripts/bootstrap-install.sh | bash
```

The default installation:

- installs webport and the pinned official Traefik release;
- uses `webport.localhost`, which resolves to loopback without DNS setup;
- creates a domain-constrained local CA;
- asks before trusting that CA in the operating-system trust store; and
- installs an unprivileged webport daemon.

From a Git checkout, wrap the normal development command:

```bash
webport dev -- npm run dev
```

webport infers the repository and branch, finds the child process's HTTP
listener, prints the HTTPS URL, keeps the route alive, and cleans it up when
the command exits. Select a listener explicitly when an application opens more
than one:

```bash
webport dev --port 5173 -- npm run dev
```

An abruptly killed client stops renewing its lease, so the route disappears
in about 30 seconds. If the daemon restarts, running clients automatically
re-register.

For an automated local installation:

```bash
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/master/scripts/bootstrap-install.sh | bash -s -- \
  --local --trust-local-ca --non-interactive --yes
```

Upgrade an existing installation to the latest release without repeating its
configuration:

```bash
webport upgrade
```

The upgrade reuses the saved domain, TLS mode, DNS provider, zone, and
credentials and replaces the installed `webport` CLI, `webportctl`, and
`webport-dns` binaries. Foreground development sessions that were already
running keep their original process until restarted; `webport inspect` can
still read their older session state. The upgrade does not change local-CA
trust unless `--trust-local-ca` is explicitly supplied.

Install the Webport development skill for Codex, OpenCode, Pi, and Claude Code
without installing or changing the Webport daemon stack:

```bash
webport install --ai-skill
```

If a skill file already exists, the command asks before replacing it. Use
`--yes` for unattended replacement.

## Commands

```text
webport install [options]                    Install or reconfigure the stack
webport install --ai-skill [--yes]           Install the Webport agent skill
webport upgrade [options]                    Upgrade using the saved configuration
webport dev [options] -- COMMAND [ARG...]    Run and publish a dev server
webport route --port PORT [options]          Publish an already-running server
webport inspect [--config PATH] [options]    Show active URLs and project environment
webport list                                 List active routes
webport status                               Show daemon/publication state
webport doctor                               Test API, TLS, trust, and DNS
webport config                               Show effective client configuration
webport dns status|sync [options]            Inspect or manage wildcard DNS
webport daemon                               Run the system daemon
webport version                              Print the version
```

`webportctl` and `webport-dns` remain available as compatibility commands.
See [MIGRATION.md](MIGRATION.md).

Common route options:

| Option | Default |
| --- | --- |
| `--project` | Git repository name |
| `--branch` | Current Git branch |
| `--port` | Inferred by `webport dev`; required by `webport route` |
| `--ttl` | `30s` |
| `--api` | `http://127.0.0.1:8080` |

To check the current project's active HTTPS URLs and environment from another
terminal, run:

```bash
webport inspect
webport inspect --format json
webport inspect --config ../other-project/.webport.yaml
```

For a configured session, `inspect` groups live environment values by service
and redacts sensitive values. Use `--show-sensitive` to reveal them or
`--include-inherited` to include inherited process variables. The `--config`
path selects a specific active worktree, even when called from another
project. A project started with `webport dev -- COMMAND` has no configured
environment; `inspect` finds its current Git route and displays the active URL
and route context derived from it. These derived values are not a reading of
the child process environment. If no session or route is active, `inspect`
reports that state; use `webport dev config` to preview an inactive configured
project. `webport list` shows all active routes, including routes started with
an explicit identity that differs from the current Git project or branch.

## Configuration-driven sessions

Commit a `.webport.yaml` to make `webport dev` resolve a complete foreground
session: named ports, generated values, environment precedence, dependencies,
readiness, route leases, logs, exports, and shutdown behavior. A reviewed
starter is available at [examples/webport.yaml](examples/webport.yaml), and
the complete v1 contract is in
[docs/configuration-driven-development-sessions.md](docs/configuration-driven-development-sessions.md).

```bash
webport dev                              # run the default profile
webport dev frontend                     # run one service and its dependencies
webport dev check                        # validate without starting commands
webport dev config --format json         # print managed session values
webport dev status --format json         # inspect live or retained state
webport dev env --shell fish             # render managed live environment
webport dev env --include-inherited      # include inherited process variables
webport dev logs backend --follow        # follow the service log
webport dev exec backend -- go test ./... # run a one-off command in the service context
webport dev stop                         # request graceful foreground shutdown
webport dev clean --secrets              # explicitly remove project secrets
```

Configured sessions are foreground-owned and do not detach. `--` always
selects the original wrapper path, so `webport dev -- npm run dev` remains
valid even when a configuration file is present. Status/config output is
redacted by default; project-lifetime generated values live only in a
mode-`0600` per-user runtime secret store keyed by the worktree and are never
written to Webport-generated diagnostics or retained session metadata.
`webport dev config` and `webport dev env` show values contributed by Webport
or the session configuration by default; pass `--include-inherited` to also
show the inherited process environment.
Configured export files intentionally contain resolved environment values while
the session is active, are mode `0600`, and are removed during clean shutdown.
Child output is mirrored as-is, so applications should avoid printing secrets.
Configured sessions keep bounded per-service logs by default in Webport's
private runtime state, making `webport dev logs SERVICE` available without a
`logs` block. Set `destination: none` to opt out, or choose `file`/`directory`
for project-controlled log paths.

## Public domains

Public ACME/DNS-01 is an advanced installation path. Create a provider token
with permission to read the authoritative zone and edit its DNS records. The
credentials file must contain the raw token—not an HTTP `Bearer` prefix:

```bash
printf 'CF_DNS_API_TOKEN=replace-with-raw-token\n' >cloudflare.env
chmod 600 cloudflare.env

curl -fsSL https://raw.githubusercontent.com/webportdev/webport/master/scripts/bootstrap-install.sh | bash -s -- \
  --public \
  --provider cloudflare \
  --base-domain dev.example.com \
  --credentials-file "$PWD/cloudflare.env" \
  --dns-ipv4 203.0.113.10 \
  --non-interactive --yes
```

Curated providers:

| Provider | Code | Credential |
| --- | --- | --- |
| Cloudflare | `cloudflare` | `CF_DNS_API_TOKEN` |
| DigitalOcean | `digitalocean` | `DO_AUTH_TOKEN` |
| Route53 | `route53` | Standard AWS environment credentials/profile |

Traefik's DNS-01 challenge creates temporary validation records. Browsers also
need a persistent `*.BASE_DOMAIN` A/AAAA record. Supply `--dns-ipv4` during
installation or manage it later:

```bash
sudo webport dns status
sudo webport dns sync --ipv4 203.0.113.10
```

Provider credentials are installed mode `0600` and are never accepted as
command-line secret values. Run `webport doctor` after changing credentials.

## Existing Traefik

Use the compatibility installer mode or its clearer alias:

```bash
webport install --external-traefik --base-domain dev.example.com
```

The existing Traefik must provide:

- a watched file-provider directory containing webport's generated YAML;
- an HTTPS entrypoint matching `WEBPORT_TRAEFIK_ENTRYPOINT`;
- the configured DNS-01 resolver in public mode; and
- read access to the generated file and local certificate, when applicable.

Unmanaged Caddy or Traefik installations are never replaced automatically.

## How route leases work

In the zero-configuration wrapper form (`webport dev -- COMMAND`):

1. webport starts the application and finds one HTTP listener.
2. It creates an opaque 30-second lease through the loopback API.
3. The daemon validates route and hostname conflicts.
4. The complete candidate Traefik document is generated and atomically
   published before the route is committed.
5. The client renews every 10 seconds.
6. Clean exit releases the lease; an unclean exit is removed by expiry.
7. A daemon restart begins empty, and running clients re-register.

Configured sessions allocate named ports and create one lease for each
configured route after that service passes readiness. Discovered ports are
filled from the owning service's listener and propagated to dependent services,
readiness checks, endpoints, and route registration.

Multiple clients can hold the same route when they agree on its backend port.
One client exiting does not remove another client's route. Conflicting ports
and identities that produce the same hostname fail with `409 Conflict`.

The daemon remains intentionally stateless. A restart publishes an empty route
set before accepting registrations, preventing stale proxy configuration.
See [docs/architecture.md](docs/architecture.md) for component, transaction,
concurrency, and privilege details.

## Process discovery migration

System-wide process discovery required the entire API daemon to run as root.
New installations disable it and run webport as a dedicated service account.
Use:

```bash
webport dev -- npm run dev
```

instead of:

```bash
WEBPORT_ROUTE='myapp:feature/auth' npm run dev
```

The wrapper preserves automatic listener selection without privileged process
inspection. Legacy discovery remains a deprecated opt-in during migration.

## HTTP API

The preferred API is lease-oriented.

Create or idempotently recover a lease:

```http
POST /v1/leases
Content-Type: application/json

{
  "client_id": "opaque-client-instance",
  "project": "myapp",
  "branch": "feature/auth",
  "port": 5173,
  "ttl": 30
}
```

Renew and release:

```http
POST /v1/leases/{lease_id}/heartbeat
DELETE /v1/leases/{lease_id}
```

Operational endpoints:

```http
GET /health
GET /ready
GET /status
GET /config
GET /routes
```

`/health` is process liveness. `/ready` returns `503` until the initial
Traefik document is applied and whenever publication is unhealthy. `/status`
reports version, uptime, route/lease counts, applied revision, and the latest
redacted publication error.

The legacy `POST /routes`, route-ID heartbeat, and delete endpoints remain
available during the v0.1.0 migration release. Branch slashes must still be
escaped when using those legacy path-based IDs.

## Configuration

The installer manages these environment values:

| Variable | Default | Description |
| --- | --- | --- |
| `WEBPORT_BASE_DOMAIN` | `webport.localhost` when installed | Hostname suffix |
| `WEBPORT_TLS_MODE` | `local-ca` when installed | `local-ca` or `acme` |
| `WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH` | `/etc/traefik/dynamic/webport.yml` | Generated document |
| `WEBPORT_TRAEFIK_ENTRYPOINT` | `websecure` | HTTPS entrypoint |
| `WEBPORT_TRAEFIK_CERT_RESOLVER` | `webport` | ACME resolver |
| `WEBPORT_LOCAL_CA_DIR` | beside generated YAML | Local CA state |
| `WEBPORT_LISTEN_HOST` | `127.0.0.1` | API host |
| `WEBPORT_PORT` | `8080` | API port |
| `WEBPORT_DEFAULT_TTL` | `30s` | Default lease lifetime |
| `WEBPORT_TTL_CHECK_INTERVAL` | `10s` | Expiration interval |
| `WEBPORT_SHUTDOWN_TIMEOUT` | `5s` | HTTP shutdown timeout |
| `WEBPORT_DISCOVERY_ENABLED` | `false` | Deprecated privileged discovery |
| `WEBPORT_DISCOVERY_INTERVAL` | `2s` | Legacy discovery reconciliation interval |

Invalid booleans, durations, ports, domains, or TLS combinations prevent
startup with explicit errors; malformed values are not silently replaced.

Linux paths:

```text
/etc/webport/webport.env
/etc/traefik/traefik.yml
/etc/traefik/dynamic/webport.yml
/var/lib/traefik/acme.json
```

macOS uses `/usr/local/etc/webport` and `/usr/local/etc/traefik`.

## Troubleshooting

Start with:

```bash
webport doctor
webport status
sudo systemctl status webport-stack.target webport.service traefik.service
sudo journalctl -u webport.service -u traefik.service -n 100 --no-pager
```

On macOS:

```bash
sudo launchctl print system/com.webport.webport
sudo launchctl print system/com.webport.traefik
tail -n 100 /usr/local/var/log/webport/webport.log
tail -n 100 /usr/local/var/log/traefik/traefik.log
```

Common diagnoses:

- `TRAEFIK DEFAULT CERT`: inspect Traefik ACME logs and provider credentials.
- Cloudflare `6111`: remove a copied `Bearer ` prefix or quotes from the raw
  token.
- Traefik 404: run `webport list`; no route means the client is not registered.
- Vite host rejection: `.localhost` names are accepted by default; custom
  public/private domains must be listed in Vite's `server.allowedHosts`.
- Browser trust warning in local mode: rerun installation with
  `--trust-local-ca`, then restart the browser if it caches trust state.

## Development

```bash
mise run all
mise run test
mise run lint
mise run installer-test
mise run installer-test-macos
mise run dev
```

Focused tests are also supported:

```bash
go test ./internal/route
go test ./cmd/webportctl/runtime
```

The CI suite runs unit tests, race detection, vet, installer tests, cross
builds, plist validation, and a real Traefik file-provider integration test.
