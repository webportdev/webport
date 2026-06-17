# webport

A service that dynamically generates and manages Caddy reverse proxy configurations for localhost development servers, providing publicly accessible HTTPS URLs with automatic cleanup.

## Features

- **Dynamic routing** - Register routes via simple HTTP REST API
- **Automatic HTTPS** - Leverages Caddy's built-in Let's Encrypt support
- **TTL-based cleanup** - Routes expire automatically after inactivity
- **Heartbeat support** - Refresh route TTLs to keep active routes alive
- **Graceful shutdown** - All routes cleared on service shutdown
- **Systemd integration** - Ready-to-run systemd service with watchdog support
- **Native macOS support** - LaunchDaemons keep Caddy and webport running without Docker networking

## Quick Start

```bash
# Interactive setup for webport and a DNS-enabled managed Caddy.
# Downloads webport binaries; uses Docker to build the custom Caddy binary.
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/main/scripts/bootstrap-install.sh | bash

# Fully automated Cloudflare setup on Linux or macOS
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/main/scripts/bootstrap-install.sh | bash -s -- \
  --mode full --provider cloudflare \
  --base-domain dev.example.com --credentials-file /root/cloudflare.env \
  --non-interactive --yes
```

## Installation

### Supported Installer

The supported install path downloads prebuilt `webport`, `webportctl`, and `webport-dns` binaries from GitHub Releases. Managed Caddy installs are built locally in Docker so users do not need a Go toolchain.

The distro-neutral installer supports Linux systems using systemd:

- `--mode webport`: install `webport` and `webportctl` against an existing Caddy. It verifies the selected DNS module and leaves the existing Caddy service and credentials untouched.
- `--mode caddy`: build and install a webport-managed Caddy with the selected DNS module. Docker is required by default.
- `--mode full`: install both managed Caddy and webport.

Provider presets are available for `cloudflare`, `digitalocean`, and `route53`. Custom providers are limited to modules accepting one token argument:

```bash
./scripts/install.sh --mode full --provider custom \
  --module-path github.com/example/caddy-dns-example \
  --module-version v1.2.3 --provider-name example \
  --token-env-var EXAMPLE_API_TOKEN --base-domain dev.example.com \
  --credentials-file /root/example.env --non-interactive --yes
```

Credential files use systemd `EnvironmentFile` syntax, for example `CLOUDFLARE_API_TOKEN=...`, `DO_AUTH_TOKEN=...`, or AWS environment credentials for Route53. Secret values are never accepted as installer arguments. Use `--dry-run` to inspect actions.

Managed Caddy installs are marked under `/etc/caddy/.webport-managed`. The installer refuses to replace an unmarked Caddy installation. It builds and validates upgrades before stopping Caddy and restores the previous managed binary if startup fails.

If you already have a Caddy binary, install it without Docker:

```bash
./scripts/install.sh --mode caddy --provider cloudflare \
  --credentials-file /root/cloudflare.env \
  --caddy-source local --artifact-dir /path/to/caddy-dir
```

The local Caddy directory can contain `caddy`, `caddy-cloudflare`, or `caddy-cloudflare-linux-amd64`.

To build only the custom Caddy binary with Docker:

```bash
./scripts/build-caddy-docker.sh --provider cloudflare --output build/caddy
sudo install -m 755 build/caddy /usr/local/bin/caddy
```

### macOS

The native macOS installer uses root-owned LaunchDaemons so Caddy can bind ports 80/443 and proxy directly to development servers on macOS localhost:

```bash
./scripts/install-macos.sh --mode full --provider cloudflare \
  --base-domain dev.example.com --credentials-file /path/to/cloudflare.env \
  --non-interactive --yes
```

It supports the same install modes and DNS providers as the Linux installer. Managed files use these paths:

- Configuration: `/usr/local/etc/webport` and `/usr/local/etc/caddy`
- LaunchDaemons: `/Library/LaunchDaemons/com.webport.{caddy,webport}.plist`
- Logs: `/usr/local/var/log/{caddy,webport}`

For `webport` and `full` installs, the interactive installer can create the persistent wildcard DNS record required for route hostnames to resolve. Non-interactive installs can request the same behavior:

```bash
./scripts/install-macos.sh --mode full --provider cloudflare \
  --base-domain dev.example.com --credentials-file /path/to/cloudflare.env \
  --dns-ipv4 203.0.113.10 --dns-ipv6 2001:db8::10 \
  --dns-zone example.com --non-interactive --yes
```

`--dns-zone` is optional; webport discovers the accessible parent zone when omitted.

Service management:

```bash
sudo launchctl kickstart -k system/com.webport.caddy
sudo launchctl kickstart -k system/com.webport.webport
tail -f /usr/local/var/log/webport/webport.log
```

To uninstall, boot out both services and remove their LaunchDaemon plists, binaries, wrappers, and `/usr/local/etc/{webport,caddy}` configuration. The installer will not replace an unmanaged Caddy installation.

### From Source

```bash
git clone https://github.com/webportdev/webport
cd webport
mise run build-all          # Build webport, webportctl, and webport-dns
mise run install-all        # Interactive native installer for macOS or Linux
```

On macOS, `install-all` delegates to `scripts/install-macos.sh` and installs LaunchDaemons. On Linux, it installs both binaries and the systemd service. Run installation commands without `sudo`; they build with your user account and request elevation only when changing system files or services. Source installs still require Go unless you pass `--webport-source release` or `--webport-source local`.

### Linux Systemd Service

```bash
# Install the systemd service file
mise run install-service

# Configure the service
sudo mkdir -p /etc/webport
sudo cp /etc/webport/webport.env.example /etc/webport/webport.env
sudo nano /etc/webport/webport.env

# Enable and start
sudo systemctl enable webport
sudo systemctl start webport
```

## Configuration

webport is configured via environment variables:

| Variable | Default | Description |
|----------|---------|-------------|
| `WEBPORT_BASE_DOMAIN` | *required* | Base domain for routes (e.g., "mond.boo") |
| `WEBPORT_CADDYFILE_PATH` | `/etc/caddy/conf.d/Caddyfile` | Output Caddyfile path |
| `WEBPORT_LISTEN_HOST` | `127.0.0.1` | HTTP API listen host |
| `WEBPORT_PORT` | `8080` | HTTP API port |
| `WEBPORT_DEFAULT_TTL` | `300s` | Default route expiration time |
| `WEBPORT_CADDY_RELOAD_CMD` | `caddy reload --config /etc/caddy/Caddyfile` | Command to reload Caddy |
| `WEBPORT_TTL_CHECK_INTERVAL` | `30s` | How often to check for expired routes |
| `WEBPORT_SHUTDOWN_TIMEOUT` | `5s` | Graceful shutdown timeout |
| `WEBPORT_TLS_DNS_PROVIDER` | *empty* | DNS provider for TLS challenges (e.g., "cloudflare") |
| `WEBPORT_TLS_DNS_PROVIDER_MODULE` | *empty* | Caddy DNS module name (e.g., "cloudflare") |
| `WEBPORT_TLS_DNS_TOKEN_ENV_VAR` | *auto* | Env var name for DNS API token (defaults to provider-specific) |
| `WEBPORT_DNS_CREDENTIALS_FILE` | *empty* | Credentials file read by `webport-dns` |
| `WEBPORT_DNS_ZONE` | *auto* | Optional authoritative zone override for `webport-dns` |

### TLS/DNS Challenge Configuration

By default, Caddy uses the HTTP challenge for certificate generation. To use DNS challenge (required for wildcard certificates), configure the following:

**Example for Cloudflare:**
```bash
# In webport service
Environment="WEBPORT_TLS_DNS_PROVIDER=cloudflare"
Environment="WEBPORT_TLS_DNS_PROVIDER_MODULE=cloudflare"
# Optional: customize the token env var name (defaults to CLOUDFLARE_API_TOKEN)
Environment="WEBPORT_TLS_DNS_TOKEN_ENV_VAR=CLOUDFLARE_API_TOKEN"

# In Caddy service - set the actual token value
Environment="CLOUDFLARE_API_TOKEN=your_actual_token_here"
```

**Using a custom token environment variable:**
```bash
# webport service - tell it which env var to reference
Environment="WEBPORT_TLS_DNS_TOKEN_ENV_VAR=MY_CF_TOKEN"

# Caddy service - set your custom env var
Environment="MY_CF_TOKEN=your_actual_token_here"
```

**Example systemd service for Caddy with token:**
```ini
# /etc/systemd/system/caddy.service
[Service]
Environment="CLOUDFLARE_API_TOKEN=your_token_here"
ExecStart=/usr/bin/caddy run --config /etc/caddy/Caddyfile
```

**Supported DNS providers:** See [Caddy's TLS documentation](https://caddyserver.com/docs/caddyfile/directives/tls#dns-providers) for the full list.

When DNS challenge is enabled without `WEBPORT_TLS_DNS_TOKEN_ENV_VAR` set:
- Cloudflare uses `CLOUDFLARE_API_TOKEN`
- DigitalOcean uses `DO_AUTH_TOKEN`
- Route53 emits `dns route53` with no token argument and reads AWS credentials from Caddy's environment
- Other providers use `{PROVIDER}_API_TOKEN`

You can override this by explicitly setting `WEBPORT_TLS_DNS_TOKEN_ENV_VAR` to any environment variable name you prefer.

When DNS challenge is enabled, webport will:
1. Add a `*.domain.com` block to request a wildcard certificate
2. Add `tls { dns <provider> {env.TOKEN_VAR} }` to each route block with the configured environment variable reference

DNS-01 creates temporary TXT records only for certificate validation. It does not create the persistent A/AAAA record browsers need to resolve route hostnames. Use the installer DNS prompts or `webport-dns` to manage that wildcard record.

**Generated Caddyfile example:**
```caddyfile
*.example.com {
    tls {
        dns cloudflare {env.CLOUDFLARE_API_TOKEN}
    }
}

myapp-main.example.com {
    tls {
        dns cloudflare {env.CLOUDFLARE_API_TOKEN}
    }
    reverse_proxy localhost:3000
}
```

### Pinned Version Maintenance

Tested Caddy, xcaddy, and provider versions live in `scripts/versions.env`.

```bash
# Report available upstream versions
./scripts/update-versions.sh

# Build and validate every curated provider, run repository tests, then update pins
./scripts/update-versions.sh --apply
```

Real certificate issuance remains a manual integration check because it requires provider credentials and public DNS.

### Example Caddy Configuration

Your main Caddyfile should include the generated config:

```caddyfile
# /etc/caddy/Caddyfile
{
    # Your global options
}

# Include webport generated routes
import conf.d/Caddyfile
```

## API Reference

### Register a Route

```bash
POST /routes
Content-Type: application/json

{
  "project": "myapp",
  "branch": "feature/auth",
  "port": 3000,
  "ttl": 600
}
```

**Response:**

```json
{
  "project": "myapp",
  "branch": "feature/auth",
  "port": 3000,
  "domain": "myapp-feature-auth.mond.boo",
  "created_at": "2025-02-16T10:30:00Z",
  "expires_at": "2025-02-16T10:40:00Z"
}
```

### List All Routes

```bash
GET /routes
```

**Response:**

```json
{
  "routes": [
    {
      "project": "myapp",
      "branch": "main",
      "port": 3000,
      "domain": "myapp-main.mond.boo",
      "created_at": "2025-02-16T10:30:00Z",
      "expires_at": "2025-02-16T10:35:00Z"
    }
  ],
  "total": 1
}
```

### Delete a Route

```bash
DELETE /routes/{project}-{branch}
```

Example: `DELETE /routes/myapp-main`

**Note:** Uses dash delimiter. For projects/branches containing dashes, the last dash separates project from branch.

### Heartbeat (Refresh TTL)

```bash
POST /routes/{project}-{branch}/heartbeat
Content-Type: application/json

{
  "ttl": 600
}
```

### Health Check

```bash
GET /health
```

Returns `200 OK` if the service is running.

### Query Server Configuration

```bash
GET /config
```

**Response:**

```json
{
  "base_domain": "mond.boo",
  "default_ttl_seconds": 300
}
```

Returns the server's base domain and default TTL configuration. Useful for clients to discover the configured base domain.

## Client

### webportctl (Recommended)

`webportctl` is a companion binary that handles route registration, heartbeat, and cleanup automatically.

```bash
# Install
mise run build-client
mise run install-client

# Run with your dev server (auto-detects project/branch from git)
webportctl -port 3000

# Or specify explicitly
webportctl -project myapp -branch main -port 3000

# Run alongside your dev server
npm run dev &
webportctl -port 3000
```

**Features:**
- Auto-detects project and branch from git repository
- Registers route on startup
- Sends periodic heartbeats to keep route alive
- Unregisters route on exit (SIGINT/SIGTERM)
- Single static binary - no dependencies

**Options:**
| Flag | Environment Variable | Default | Description |
|------|---------------------|---------|-------------|
| `-port` | `APP_PORT` | *required* | Local app port to proxy to |
| `-project` | `WEBPORT_PROJECT` | git repo basename | Project name |
| `-branch` | `WEBPORT_BRANCH` | current git branch | Branch name |
| `-ttl` | `WEBPORT_TTL` | 300 | Route TTL in seconds |
| `-api` | `WEBPORT_API_ADDR` | localhost:8080 | Webport API address |
| `-interval` | `WEBPORT_INTERVAL` | 60 | Heartbeat interval in seconds |
| `-v` | `WEBPORT_VERBOSE` | false | Verbose mode (log heartbeat messages) |
| `-query-config` | - | false | Query server config and exit |

**Querying server configuration:**

```bash
# Query the server for its base domain and default TTL
webportctl -query-config
# Output:
# Base Domain: mond.boo
# Default TTL: 300 seconds
```

**Integration with package.json:**

```json
{
  "scripts": {
    "dev": "concurrently \"npm run dev:server\" \"webportctl -port 3000\"",
    "dev:server": "vite"
  },
  "devDependencies": {
    "concurrently": "^8.2.0"
  }
}
```

## Client Examples

### curl

```bash
# Register a route
curl -X POST http://localhost:8080/routes \
  -H "Content-Type: application/json" \
  -d '{
    "project": "myapp",
    "branch": "main",
    "port": 3000
  }'

# List routes
curl http://localhost:8080/routes

# Delete a route
curl -X DELETE http://localhost:8080/routes/myapp-main

# Send heartbeat
curl -X POST http://localhost:8080/routes/myapp-main/heartbeat
```

### Go

```go
package main

import (
    "bytes"
    "encoding/json"
    "net/http"
)

type RegisterRequest struct {
    Project string `json:"project"`
    Branch  string `json:"branch"`
    Port    int    `json:"port"`
    TTL     int    `json:"ttl,omitempty"`
}

func registerRoute(project, branch string, port int) error {
    req := RegisterRequest{
        Project: project,
        Branch:  branch,
        Port:    port,
        TTL:     600,
    }

    body, _ := json.Marshal(req)
    resp, err := http.Post(
        "http://localhost:8080/routes",
        "application/json",
        bytes.NewReader(body),
    )
    if err != nil {
        return err
    }
    defer resp.Body.Close()
    return nil
}
```

### Node.js

```javascript
async function registerRoute(project, branch, port) {
  const response = await fetch('http://localhost:8080/routes', {
    method: 'POST',
    headers: {
      'Content-Type': 'application/json',
    },
    body: JSON.stringify({
      project,
      branch,
      port,
      ttl: 600,
    }),
  });

  if (!response.ok) {
    throw new Error(`HTTP error! status: ${response.status}`);
  }

  return await response.json();
}

// Usage
registerRoute('myapp', 'main', 3000)
  .then(data => console.log(data))
  .catch(error => console.error(error));
```

### Python

```python
import requests

def register_route(project, branch, port):
    url = "http://localhost:8080/routes"
    data = {
        "project": project,
        "branch": branch,
        "port": port,
        "ttl": 600
    }
    response = requests.post(url, json=data)
    response.raise_for_status()
    return response.json()

# Usage
route = register_route("myapp", "main", 3000)
print(route)
```

## How It Works

1. **Route Registration** - Services call POST /routes with their project name, git branch, and port (or use webportctl)
2. **Caddyfile Generation** - webport generates a Caddyfile with reverse_proxy entries for each route
3. **Caddy Reload** - The generated Caddyfile is written atomically and Caddy is reloaded
4. **TTL Tracking** - Each route has an expiration time (default 5 minutes)
5. **Heartbeat** - Services can call the heartbeat endpoint to refresh their TTL (or use webportctl)
6. **Cleanup** - Expired routes are automatically removed and Caddy is reloaded

```
┌─────────────┐     POST /routes     ┌─────────────┐
│  Dev Server │ ────────────────────> │   webport   │
│  :3000      │    (or webportctl) │   :8080     │
└─────────────┘                       └──────┬──────┘
                                             │
                                             │ Generates
                                             ▼
                                     ┌──────────────┐
                                     │ Caddyfile    │
                                     │ + reload     │
                                     └──────┬───────┘
                                            │
                                            ▼
                                    ┌──────────────┐
                                    │    Caddy     │
                                    └──────┬───────┘
                                           │
                              HTTPS: myapp-main.mond.boo
                                           │
                                    ┌──────▼───────┐
                                    │  Internet    │
                                    └──────────────┘
```

## Binary Distribution

webport consists of two binaries:

- **webport** - The server that manages routes and generates Caddy configurations
- **webportctl** - A client that runs alongside your dev server to maintain routes
- **webport-dns** - A privileged administration command for wildcard A/AAAA records

Build all three with:
```bash
mise run build-all
```

## Wildcard DNS Management

The installer can configure DNS during setup. To inspect or update it later:

```bash
# Show managed wildcard A/AAAA records
sudo webport-dns status

# Replace wildcard records with a new public IPv4 and optional IPv6
sudo webport-dns sync --ipv4 203.0.113.10 --ipv6 2001:db8::10

# Override automatic authoritative-zone discovery
sudo webport-dns sync --ipv4 203.0.113.10 --zone example.com
```

The wildcard records use a 300-second TTL and are DNS-only. Your router/firewall must forward public ports 80 and 443 to the webport host.

## Troubleshooting

### Go cache contains root-owned files

Installation commands must be run without an outer `sudo`. If an earlier install left root-owned entries in your Go caches, repair their ownership once:

```bash
sudo chown -R "$(id -u):$(id -g)" "$(go env GOMODCACHE)" "$(go env GOCACHE)"
```

### Service won't start

```bash
# Check service status
sudo systemctl status webport

# View logs
sudo journalctl -u webport -n 50

# Check if port is already in use
sudo ss -tlnp | grep 8080
```

### Routes not accessible

1. Verify Caddy is running: `sudo systemctl status caddy`
2. Check the generated Caddyfile: `sudo cat /etc/caddy/webport.d/Caddyfile`
3. Ensure your main Caddyfile imports the webport config
4. Run `sudo webport-dns status`
5. Query public DNS for a route hostname, for example `dig +short app-main.dev.example.com`
6. Verify ports 80 and 443 reach the webport host

If Caddy logs show that a wildcard certificate was issued but the hostname returns no DNS address, the DNS-01 challenge succeeded while the persistent wildcard A/AAAA record is missing. Run `sudo webport-dns sync`.

### Rotate credentials after upgrading

Earlier managed Caddy installations started Caddy with `--environ`, which wrote provider credentials to Caddy logs. Rotate any affected Cloudflare, DigitalOcean, or AWS credentials after upgrading, replace the installed Caddy credentials file, and restart Caddy.

### DNS challenge fails with "missing API token"

The generated Caddyfile references environment variables for API tokens. You need to set the token in Caddy's environment:

```bash
# Edit Caddy service to add the token
sudo systemctl edit caddy

# Add:
[Service]
Environment="CLOUDFLARE_API_TOKEN=your_token_here"

# Reload Caddy
sudo systemctl daemon-reload
sudo systemctl restart caddy
```

### Routes disappearing too quickly

Increase the TTL:

```bash
curl -X POST http://localhost:8080/routes/myapp-main/heartbeat \
  -H "Content-Type: application/json" \
  -d '{"ttl": 3600}'
```

Or change the default in the systemd service file:

```ini
Environment="WEBPORT_DEFAULT_TTL=3600s"
```

## Development

```bash
# Build the server
mise run build

# Build the client
mise run build-client

# Build both
mise run build-all

# Run in development mode (doesn't modify system Caddy)
mise run dev

# Run tests
mise run test

# Format code
mise run fmt
```

## License

MIT
