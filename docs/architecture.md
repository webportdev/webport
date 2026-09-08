# Architecture

## Components

- `webport daemon` owns the ephemeral lease registry, validation, REST API,
  local CA lifecycle, and Traefik file publication.
- `webport dev` runs in the developer's user session. It supervises one
  process tree, discovers its listener without elevated privileges, and owns a
  renewable route lease.
- A configured `webport dev` session resolves a worktree-scoped plan before
  launching commands, then owns the dependency graph, environment, ports,
  readiness checks, route leases, logs, exports, authenticated control socket,
  and graceful shutdown in the foreground process.
- Traefik owns ports 80/443, public ACME state, and HTTPS proxying.
- `webport dns` performs explicit persistent wildcard A/AAAA administration.

The system has no persistent route database. Configuration and certificate
state are persistent; active routes are reconstructed by clients.

Configured-session runtime state is also intentionally ephemeral: a per-user
worktree lock, live state file, and local control socket exist only while the
foreground session runs. A redacted last-session record may remain for
inspection. Project-lifetime generated secrets are kept separately in the
per-user runtime state directory with mode `0600`; they are not copied into session
state, logs, exports after shutdown, or summaries.

## Route transaction

```text
client request
    ↓
validate identity, port, lease, and hostname collisions
    ↓
construct complete candidate route snapshot
    ↓
generate deterministic Traefik YAML
    ↓
sync + atomic rename
    ↓
commit registry state and applied revision
```

A publication error leaves both the prior registry state and prior Traefik
document active. Readiness reports the failure.

Heartbeats only update lease expiry because expiration timestamps are not part
of Traefik configuration. Releasing or expiring the final lease for a route
does require a publication transaction.

## Lease behavior

- Lease IDs are random and opaque.
- Client IDs make create requests idempotent for one daemon lifetime.
- Multiple leases may claim one identity only when their port agrees.
- An effective route exists while at least one lease remains active.
- Legacy `/routes` registrations are represented internally as synthetic
  leases.
- Route identities and generated hostnames are separate concepts; collisions
  are detected rather than reverse-parsed.

## Concurrency

The controller mutex serializes effective-route mutations and publication.
The underlying store retains its own read/write lock for snapshots consumed by
the API and Traefik generator. Process scanning is not part of the default
daemon path.

## Privilege boundary

The webport daemon runs as a dedicated service account and listens only on
loopback. Its writable scope is limited to the managed Traefik dynamic
directory and local-CA state. Traefik runs separately with only the capability
needed to bind privileged ports.

The local CA private key is readable only by webport. The wildcard private key
is group-readable by Traefik. Provider credentials are read by Traefik from a
mode-`0600` environment file and never enter generated dynamic YAML.

## Failure recovery

- Client crash: its lease expires.
- Daemon restart: initial empty publication removes stale routes; running
  clients re-register after heartbeat `404`.
- Traefik restart: persistent certificate state is reused and the watched
  dynamic document is loaded.
- Publication failure: the candidate state is rolled back and readiness fails.
- Machine reboot: services start empty and active developer clients recreate
  routes when they resume.
