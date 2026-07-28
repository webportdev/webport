# webport

webport dynamically publishes Traefik routes for localhost development
servers, giving them public HTTPS hostnames with automatic cleanup.

A route such as:

```text
myapp-feature-auth.dev.example.com -> http://127.0.0.1:3000
```

is registered through a small localhost REST API. Routes expire unless their
clients send heartbeats.

## Features

- Dynamic HTTPS routing through Traefik's watched file provider
- One wildcard ACME certificate using any DNS provider built into Traefik/Lego
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

Installer modes:

- `webport`: install webport against an existing Traefik installation.
- `traefik`: install only a webport-managed Traefik.
- `full`: install both.

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
| `WEBPORT_LISTEN_HOST` | `127.0.0.1` | REST API listen host |
| `WEBPORT_PORT` | `8080` | REST API port |
| `WEBPORT_DEFAULT_TTL` | `300s` | Default route lifetime |
| `WEBPORT_TTL_CHECK_INTERVAL` | `30s` | Expiration scan interval |
| `WEBPORT_SHUTDOWN_TIMEOUT` | `5s` | HTTP shutdown timeout |
| `WEBPORT_DNS_PROVIDER` | empty | Provider used by `webport-dns` |
| `WEBPORT_DNS_CREDENTIALS_FILE` | empty | Credentials used by `webport-dns` |
| `WEBPORT_DNS_ZONE` | auto | Optional authoritative zone override |

Managed Linux paths:

- Traefik configuration: `/etc/traefik`
- Traefik ACME state: `/var/lib/traefik/acme.json`
- webport configuration: `/etc/webport/webport.env`
- Services: `traefik.service` and `webport.service`

Managed macOS paths:

- Traefik configuration: `/usr/local/etc/traefik`
- Traefik ACME state: `/usr/local/var/lib/traefik/acme.json`
- webport configuration: `/usr/local/etc/webport/webport.env`
- LaunchDaemons: `com.webport.traefik` and `com.webport.webport`

### Existing Traefik

`--mode webport` expects the existing Traefik installation to provide:

- A watched file-provider directory containing the configured
  `WEBPORT_TRAEFIK_DYNAMIC_CONFIG_PATH`.
- An HTTPS entrypoint matching `WEBPORT_TRAEFIK_ENTRYPOINT`.
- A DNS-01 ACME resolver matching `WEBPORT_TRAEFIK_CERT_RESOLVER`.
- Permission to read the generated configuration file.

The generated dynamic configuration defines routers/services and a default
wildcard certificate for `*.WEBPORT_BASE_DOMAIN`.

## Client

`webportctl` runs alongside a development server. It detects the Git project
and branch, registers the route, sends heartbeats, and unregisters on exit:

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

`GET /config` returns the base domain and default TTL.

## How it works

1. A development process registers its project, branch, and port.
2. webport stores the route and atomically replaces its generated Traefik YAML.
3. Traefik's file provider detects and applies the new router without restart.
4. Traefik serves the route with the managed wildcard certificate.
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
sudo systemctl status traefik webport
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

If HTTPS works but a hostname does not resolve, inspect the persistent wildcard
record with `sudo webport-dns status`. If Traefik cannot obtain a certificate,
check its logs and verify the provider-specific environment variables in the
mode-`0600` Traefik credentials file.
