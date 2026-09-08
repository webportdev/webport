# Verification checklist

## Automated checks

```bash
GOCACHE=/tmp/webport-test-cache go test -race ./...
GOCACHE=/tmp/webport-vet-cache go vet ./...

# Installation suites are needed when installer/service assets change.
# ./scripts/install_test.sh
# ./scripts/install_macos_test.sh
```

Also verify supported builds:

```bash
for os in linux darwin; do
  for arch in amd64 arm64; do
    CGO_ENABLED=0 GOOS="$os" GOARCH="$arch" \
      go build -o "/tmp/webport-$os-$arch" ./cmd/webport
  done
done
```

## Local-first installation

- [ ] Installing with no mode/TLS/domain options selects `full`,
  `local-ca`, and `webport.localhost`.
- [ ] Interactive installation explicitly asks before trusting the CA.
- [ ] Noninteractive local installation fails without `--trust-local-ca`.
- [ ] `webport.service` runs as the dedicated `webport` account on Linux.
- [ ] `com.webport.webport` runs as `_webport` on macOS.
- [ ] The daemon can write the dynamic Traefik file and CA material.
- [ ] Traefik can read the wildcard key but not the CA private key.
- [ ] `webport doctor` passes API, TLS, trust, and wildcard-resolution checks.

## Development workflow

Run an HTTP server from a Git checkout:

```bash
webport dev -- python3 -m http.server 3000
```

- [ ] The command infers project and branch.
- [ ] It prints an HTTPS `*.webport.localhost` URL.
- [ ] The route appears in `webport list`.
- [ ] HTTPS proxies successfully to the child.
- [ ] Ctrl-C forwards to the child and removes the route.
- [ ] `kill -9` of the wrapper leaves the app route for no more than about
  30 seconds.
- [ ] Multiple listeners produce an actionable `--port` error.

## Restart recovery

With `webport dev` running:

```bash
sudo systemctl restart webport-stack.target
```

- [ ] The route briefly disappears when the daemon publishes its empty initial
  snapshot.
- [ ] The client observes a missing lease and re-registers without restarting
  the application.
- [ ] The route and HTTPS response recover automatically.
- [ ] No repeated heartbeat `404` warning remains.

## Lease semantics

- [ ] Two clients with the same identity and port share one effective route.
- [ ] Releasing one client does not remove the other.
- [ ] The last release removes the Traefik router.
- [ ] A second client using a different port receives `409 Conflict`.
- [ ] `feature/auth` and `feature-auth` cannot simultaneously claim the same
  hostname.
- [ ] A custom legacy TTL remains unchanged by an empty heartbeat body.

## Publication failure

- [ ] A failed atomic write does not commit the candidate route state.
- [ ] `/ready` returns `503`.
- [ ] `/status` contains a redacted `last_error` and different desired/applied
  revisions.
- [ ] A later successful publication restores readiness.

## Public ACME

- [ ] `Bearer TOKEN`, quoted tokens, and whitespace-padded Cloudflare tokens
  fail before installation changes services.
- [ ] A raw authorized token obtains `*.BASE_DOMAIN`.
- [ ] `webport dns status` finds the persistent wildcard record.
- [ ] `webport doctor` verifies the trusted public certificate and resolution.
- [ ] No provider secret appears in process arguments, logs, status output, or
  generated Traefik YAML.

## Compatibility

- [ ] `webportctl -port 3000` registers through the lease API.
- [ ] The client falls back to legacy `/routes` against an older daemon.
- [ ] `webportctl -query-config` and `webport-dns status|sync` still work.
- [ ] Existing legacy REST endpoints retain their response shapes.
- [ ] `webport --help`, all subcommand help, and every `version` command work
  without daemon environment configuration.

## Configuration-driven development sessions

From two separate temporary Git worktrees, create a `.webport.yaml` with a
default profile, one-shot setup service, long-running backend/frontend
services, named ports, route exports, generated project secret, exports, and
plain file logs. Then verify:

- [ ] `webport dev check` validates the plan without starting a child.
- [ ] `webport dev config --format json` is deterministic and redacted.
- [ ] Two worktrees run concurrently with distinct scopes, locks, ports,
  routes, logs, exports, and project-secret stores.
- [ ] `webport dev status --format json` exposes operational facts but no
  control token or generated secret.
- [ ] `webport dev env` redacts sensitive values by default and
  `--show-sensitive` is required for an interactive reveal.
- [ ] `webport dev logs SERVICE` reads plain output and `--follow` continues
  across bounded log rotation.
- [ ] `webport dev exec SERVICE -- COMMAND` uses the service directory and
  resolved environment without entering a container implicitly.
- [ ] `webport dev stop` performs graceful, bounded shutdown and removes live
  state/exports while retaining logs and the redacted final record.
- [ ] `webport dev clean --secrets` removes project-lifetime secrets only
  after the session is inactive, including when cleanup races a new launch.
- [ ] Existing `webport dev -- COMMAND` behavior is unchanged with or without
  a configuration file.
