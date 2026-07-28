# webport

webport dynamically publishes Traefik routes for localhost development
servers, giving them HTTPS hostnames with automatic cleanup.

A route such as:

```text
myapp-feature-auth.dev.example.com -> http://127.0.0.1:3000
```

is registered through a small localhost REST API. Routes expire unless their
clients send heartbeats.

## Features

- Dynamic HTTPS routing through Traefik's watched file provider
- One wildcard ACME certificate using any DNS provider built into Traefik/Lego
- Optional private-domain TLS using a webport-managed local CA
- TTL expiration, heartbeat refresh, and graceful cleanup
- Linux systemd and native macOS LaunchDaemon support
- Optional wildcard A/AAAA management for Cloudflare, DigitalOcean, and Route53
- Static `webport`, `webportctl`, and `webport-dns` binaries

## Installation

Run the bootstrap installer as a regular user:

```bash
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/main/scripts/bootstrap-install.sh | bash
```

The installer downloads webport release artifacts and the pinned official
Traefik binary, verifies published checksums, and requests elevation only for
system changes.

Example non-interactive Cloudflare installation:

```bash
printf 'CF_DNS_API_TOKEN=replace-me\n' >cloudflare.env
chmod 600 cloudflare.env

curl -fsSL https://raw.githubusercontent.com/webportdev/webport/main/scripts/bootstrap-install.sh | bash -s -- \
  --mode full \
  --provider cloudflare \
  --base-domain dev.example.com \
  --credentials-file "$PWD/cloudflare.env" \
  --non-interactive --yes
```

For local-only development, no public domain or DNS provider is needed:

```bash
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/main/scripts/bootstrap-install.sh | bash -s -- \
  --mode full \
  --tls-mode local-ca \
  --base-domain webport.localhost \
  --non-interactive --yes
```

On macOS, add `--trust-local-ca` to explicitly install the generated root in
the System Keychain:

```bash
./scripts/install-macos.sh \
  --mode full \
  --tls-mode local-ca \
  --base-domain webport.localhost \
  --trust-local-ca \
  --non-interactive --yes
```

Installer modes:

- `webport`: install webport against an existing Traefik installation.
- `traefik`: install only a webport-managed Traefik.
- `full`: install both.

On Linux, `webport` and `full` installations enable
`webport-stack.target`. It manages both services as one operational unit while
preserving their separate users, logs, health checks, and restart policies:

```bash
sudo systemctl start webport-stack.target
sudo systemctl stop webport-stack.target
sudo systemctl restart webport-stack.target
sudo systemctl status webport-stack.target
```

`--mode traefik` continues to enable `traefik.service` directly when no
existing Webport installation is being migrated.

Managed Traefik supports any provider code available in its bundled Lego
version. Unknown/generic providers require a credentials file containing the
provider's documented environment variables.

The curated providers use:

| Provider | Code | Primary credentials |
| --- | --- | --- |
| Cloudflare | `cloudflare` | `CF_DNS_API_TOKEN` |
| DigitalOcean | `digitalocean` | `DO_AUTH_TOKEN` |
| Route53 | `route53` | Standard AWS environment credentials/profile |

Provider credentials are installed mode `0600`. Secret values are never
accepted as command-line arguments.

### Private-domain/local-CA mode

`--tls-mode local-ca` creates a persistent ECDSA root CA and a rotating
wildcard certificate for `WEBPORT_BASE_DOMAIN`. Traefik reads the wildcard
certificate through its
[file-provider TLS configuration](https://doc.traefik.io/traefik/reference/routing-configuration/http/tls/tls-certificates/);
ACME, public DNS, and provider credentials are not used.

`webport.localhost` is the recommended local-only base domain. The
[IETF special-use rules for `localhost`](https://www.rfc-editor.org/rfc/rfc6761#section-6.3)
apply to names beneath it and keep them on the loopback interface. For example:

```text
https://myapp-main.webport.localhost
```

After installation, trust only the public `ca.crt`—never `ca.key`.
`--trust-local-ca` performs the macOS step below, is idempotent for an
unchanged CA, and replaces only Webport's domain-specific Keychain certificate
after CA rotation.

Linux systems using `update-ca-certificates`:

```bash
sudo install -m 0644 /etc/traefik/dynamic/webport-pki/ca.crt \
  /usr/local/share/ca-certificates/webport-local-ca.crt
sudo update-ca-certificates
```

macOS:

```bash
sudo security add-trusted-cert -d -r trustRoot \
  -k /Library/Keychains/System.keychain \
  /usr/local/etc/traefik/dynamic/webport-pki/ca.crt
```

Some browsers and language runtimes use a separate trust store and may require
importing `ca.crt` there as well. `webportctl -query-config` prints the active
TLS mode and CA path.

For access from other devices, use a private DNS resolver with a wildcard
record pointing at the Traefik host and choose a private base domain. Public
DNS is still unnecessary, but every client must use that resolver and trust
the CA. Avoid `.local`, which is reserved for multicast DNS.

The CA is constrained to the configured base domain. Its private key is mode
`0600`; Traefik receives only the mode-`0640` wildcard key. The root is reused
for ten years, while the leaf certificate is renewed automatically before its
90-day lifetime expires. Changing the base domain creates a new CA, which must
be trusted again.

### Configure wildcard DNS

DNS-01 creates temporary validation records but does not create the persistent
wildcard A/AAAA record browsers use to resolve route hostnames. For the curated
providers, the installer can configure it:

```bash
./scripts/install.sh \
  --mode full \
  --provider cloudflare \
  --base-domain dev.example.com \
  --credentials-file cloudflare.env \
  --dns-ipv4 203.0.113.10 \
  --dns-ipv6 2001:db8::10
```

It can also be managed later:

```bash
sudo webport-dns status
sudo webport-dns sync --ipv4 203.0.113.10 --ipv6 2001:db8::10
```

### Existing installations

The installer automatically migrates a Caddy installation carrying webport's
managed marker. It stops Caddy, installs and health-checks Traefik, translates
the former Cloudflare token name, and removes the managed Caddy installation
only after Traefik starts successfully. A failed Traefik start restores Caddy.

Unmanaged Caddy or Traefik installations are never replaced. Use `--mode
webport` for an existing Traefik, or remove conflicting software manually.

This is a breaking, Traefik-only release. Caddy-era installer flags and
`WEBPORT_CADDY*`/`WEBPORT_TLS_DNS*` environment variables are not accepted.

### Source checkout

```bash
git clone https://github.com/webportdev/webport
cd webport
mise run build-all
mise run install-all
```

Supported platforms are Linux and macOS on amd64 and arm64.

## Configuration

webport reads:

| Variable | Default | Description |
| --- | --- | --- |
| `WEBPORT_BASE_DOMAIN` | required | Base domain for generated hostnames |
| `WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH` | `/etc/traefik/dynamic/webport.yml` | Generated file-provider configuration |
| `WEBPORT_TRAEFIK_ENTRYPOINT` | `websecure` | HTTPS entrypoint for generated routers |
| `WEBPORT_TRAEFIK_CERT_RESOLVER` | `webport` | Resolver used for the wildcard certificate |
| `WEBPORT_TLS_MODE` | `acme` | TLS source: `acme` or `local-ca` |
| `WEBPORT_LOCAL_CA_DIR` | beside generated Traefik config | Local CA and wildcard certificate directory |
| `WEBPORT_LISTEN_HOST` | `127.0.0.1` | REST API listen host |
| `WEBPORT_PORT` | `8080` | REST API port |
| `WEBPORT_DEFAULT_TTL` | `300s` | Default route lifetime |
| `WEBPORT_TTL_CHECK_INTERVAL` | `30s` | Expiration scan interval |
| `WEBPORT_SHUTDOWN_TIMEOUT` | `5s` | HTTP shutdown timeout |
| `WEBPORT_DISCOVERY_ENABLED` | `true` | Discover opted-in development processes on Linux and macOS |
| `WEBPORT_DISCOVERY_INTERVAL` | `2s` | Process discovery reconciliation interval |
| `WEBPORT_DNS_PROVIDER` | empty | Provider used by `webport-dns` |
| `WEBPORT_DNS_CREDENTIALS_FILE` | empty | Credentials used by `webport-dns` |
| `WEBPORT_DNS_ZONE` | auto | Optional authoritative zone override |

Managed Linux paths:

- Traefik configuration: `/etc/traefik`
- Traefik ACME state: `/var/lib/traefik/acme.json`
- Local CA state: `/etc/traefik/dynamic/webport-pki`
- webport configuration: `/etc/webport/webport.env`
- Stack lifecycle: `webport-stack.target`
- Component services: `traefik.service` and `webport.service`

Managed macOS paths:

- Traefik configuration: `/usr/local/etc/traefik`
- Traefik ACME state: `/usr/local/var/lib/traefik/acme.json`
- Local CA state: `/usr/local/etc/traefik/dynamic/webport-pki`
- webport configuration: `/usr/local/etc/webport/webport.env`
- LaunchDaemons: `com.webport.traefik` and `com.webport.webport`

### Existing Traefik

`--mode webport` expects the existing Traefik installation to provide:

- A watched file-provider directory containing the configured
  `WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH`.
- An HTTPS entrypoint matching `WEBPORT_TRAEFIK_ENTRYPOINT`.
- In `acme` mode, a DNS-01 resolver matching
  `WEBPORT_TRAEFIK_CERT_RESOLVER`.
- In `local-ca` mode, permission for Traefik to read the generated wildcard
  certificate and key.
- Permission to read the generated configuration file.

The generated dynamic configuration defines routers/services and a default
wildcard certificate for `*.WEBPORT_BASE_DOMAIN`.

When using an existing Traefik under a nonstandard service account, make
`WEBPORT_LOCAL_CA_DIR` a setgid directory owned by that account's group so it
can read `wildcard.key`. A custom directory outside the packaged dynamic
directory must also be added to the service sandbox's writable paths for
webport.

## Process discovery

On Linux and macOS, a development server can opt into webport without running
`webportctl` or sending heartbeats:

```bash
WEBPORT_ROUTE='myapp:feature/auth' npm run dev
```

`WEBPORT_ROUTE` uses the same unambiguous `project:branch` identity as the
REST API. The daemon finds TCP listeners owned by the tagged process, probes
them for HTTP, and publishes:

```text
myapp-feature-auth.dev.example.com -> the discovered listener
```

This works with development servers such as Vite that choose another port
when their preferred port is occupied. webport inspects the actual listening
socket; it does not assume port 5173 or parse console output.

If the process owns more than one HTTP listener, select one explicitly:

```bash
WEBPORT_ROUTE='myapp:feature/auth' \
WEBPORT_APP_PORT=5173 \
npm run dev
```

The explicit port is accepted only when the tagged process owns it. A manual
API route takes precedence over a discovered route with the same identity,
and multiple processes claiming one identity fail closed rather than letting
scan order decide the winner. Discovered routes disappear after the process
is absent from two consecutive scans.

Linux reads process environments and sockets directly from `/proc`. macOS
reads opted-in process environments through `kern.procargs2` and obtains
listening socket ownership from the machine-readable output of the system
`lsof`. Keep using `webportctl` for containers and PID namespaces the daemon
cannot inspect, or when explicit registration is preferable.

### Vite allowed hosts

Vite validates the HTTP `Host` header. Add only the webport hostnames required
by the project; do not set `server.allowedHosts` to `true`, because Vite warns
that doing so permits DNS-rebinding attacks. See Vite's official
[`server.allowedHosts` documentation](https://vite.dev/config/server-options.html#server-allowedhosts).

Vite allows `localhost` and its subdomains by default, so the recommended
`*.webport.localhost` local-CA names need no additional allowed-host entry.
Custom private domains still need the configuration below.

For one hostname, Vite provides its own environment variable:

```bash
WEBPORT_ROUTE='myapp:feature/auth' \
__VITE_ADDITIONAL_SERVER_ALLOWED_HOSTS='myapp-feature-auth.dev.example.com' \
npm run dev
```

For a comma-separated project variable, configure Vite programmatically and
apply it only to the development server:

```ts
// vite.config.ts
import { defineConfig } from 'vite'

const webportAllowedHosts = (process.env.WEBPORT_ALLOWED_HOSTS ?? '')
  .split(',')
  .map((host) => host.trim())
  .filter(Boolean)

export default defineConfig(({ command }) => ({
  server:
    command === 'serve' && webportAllowedHosts.length > 0
      ? { allowedHosts: webportAllowedHosts }
      : undefined,
}))
```

Run it with:

```bash
WEBPORT_ROUTE='myapp:feature/auth' \
WEBPORT_ALLOWED_HOSTS='myapp-feature-auth.dev.example.com' \
npm run dev
```

Vite documents its automatic port fallback under
[`server.port`](https://vite.dev/config/server-options.html#server-port).

## Client

`webportctl` remains available when process discovery is not suitable. It
detects the Git project and branch, registers the route, sends heartbeats, and
unregisters on exit:

```bash
webportctl -port 3000
webportctl -project myapp -branch feature/auth -port 3000
```

Common options:

| Flag | Environment | Default |
| --- | --- | --- |
| `-port` | `APP_PORT` | required |
| `-project` | `WEBPORT_PROJECT` | Git repository name |
| `-branch` | `WEBPORT_BRANCH` | Current Git branch |
| `-ttl` | `WEBPORT_TTL` | `300` seconds |
| `-api` | `WEBPORT_API_ADDR` | `localhost:8080` |
| `-interval` | `WEBPORT_INTERVAL` | `60` seconds |

Example package script:

```json
{
  "scripts": {
    "dev": "concurrently \"vite\" \"webportctl -port 3000\""
  }
}
```

## HTTP API

### Register or update a route

```http
POST /routes
Content-Type: application/json

{
  "project": "myapp",
  "branch": "feature/auth",
  "port": 3000,
  "ttl": 600
}
```

### List routes

```http
GET /routes
```

### Delete a route

```http
DELETE /routes/{project}:{branch}
```

Branch slashes must be URL-escaped:

```text
DELETE /routes/myapp:feature%2Fauth
```

### Refresh a route

```http
POST /routes/{project}:{branch}/heartbeat
Content-Type: application/json

{"ttl": 600}
```

The body is optional.

### Service information

```http
GET /health
GET /config
```

`GET /config` returns the base domain, default TTL, TLS mode, and—when
applicable—the public CA certificate path.

## How it works

1. A development process registers its project, branch, and port.
2. webport stores the route and atomically replaces its generated Traefik YAML.
3. Traefik's file provider detects and applies the new router without restart.
4. Traefik serves the route with the ACME or local-CA wildcard certificate.
5. Heartbeats extend the route TTL.
6. Expired or explicitly deleted routes disappear from the generated file.
7. On shutdown, webport publishes an empty route set to prevent stale proxies.

## Development

```bash
mise run build
mise run test
mise run installer-test
mise run installer-test-macos
mise run dev
```

`mise run dev` writes generated Traefik configuration to
`/tmp/webport-traefik.yml` and does not modify the system proxy.

Version maintenance:

```bash
./scripts/update-versions.sh
./scripts/update-versions.sh --apply
```

Apply mode downloads and checksum-validates every supported official Traefik
asset before updating the pin.

## Troubleshooting

Linux logs and status:

```bash
sudo systemctl status webport-stack.target
sudo systemctl status traefik.service webport.service
sudo journalctl -u traefik -u webport -f
curl -fsS http://127.0.0.1:8082/ping
```

macOS:

```bash
sudo launchctl kickstart -k system/com.webport.traefik
sudo launchctl kickstart -k system/com.webport.webport
tail -f /usr/local/var/log/traefik/traefik.log
tail -f /usr/local/var/log/webport/webport.log
```

To run the real macOS discovery integration test:

```bash
WEBPORT_RUN_LIVE_DISCOVERY_TEST=1 go test ./internal/discovery \
  -run TestLiveProcessDiscovery -count=1 -v
```

In ACME mode, if HTTPS works but a hostname does not resolve, inspect the
persistent wildcard record with `sudo webport-dns status`. If Traefik cannot
obtain a certificate, check its logs and verify the provider-specific
environment variables in the mode-`0600` Traefik credentials file.

In local-CA mode, use `webportctl -query-config` to locate `ca.crt`. A browser
certificate warning means the root is not trusted by that client; a name
resolution error means the chosen private domain is not configured in its
resolver. `*.localhost` needs no public DNS.
