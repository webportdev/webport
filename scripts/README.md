# webport Client Scripts

For supported system installation, use the bootstrap installer. It downloads
webport release binaries and DNS-enabled managed Caddy binaries:

```bash
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/main/scripts/bootstrap-install.sh | bash
```

From a source checkout, use the native installers directly:

```bash
./scripts/install.sh        # Linux systemd
./scripts/install-macos.sh  # macOS LaunchDaemons
```

Both installers can configure persistent wildcard DNS with `--dns-ipv4`, optional `--dns-ipv6`, and optional `--dns-zone`. Later updates use the installed `sudo webport-dns sync` command.

To build only the custom Caddy binary without installing services:

```bash
./scripts/build-caddy-docker.sh --provider cloudflare --output build/caddy
```

Pinned dependency updates are reported by `./scripts/update-versions.sh`; its `--apply` mode validates all curated provider builds before changing the manifest.

Bash scripts for interacting with the webport API from a development environment.

**Note:** For most use cases, consider using `webportctl` instead - it's a single binary that handles registration, heartbeat, and cleanup automatically.

## Scripts

### register-route.sh
Register a new route or update an existing one.

```bash
# Minimal usage (auto-detects project and branch from git)
APP_PORT=3000 ./scripts/register-route.sh

# Full control
WEBPORT_PROJECT="myapp" WEBPORT_BRANCH="main" APP_PORT=3000 WEBPORT_TTL=600 ./scripts/register-route.sh
```

### heartbeat-route.sh
Refresh a route's TTL to prevent expiration.

```bash
# Auto-detect from git
./scripts/heartbeat-route.sh

# Explicit values
WEBPORT_PROJECT="myapp" WEBPORT_BRANCH="main" ./scripts/heartbeat-route.sh
```

### unregister-route.sh
Remove a route.

```bash
# Auto-detect from git
./scripts/unregister-route.sh

# Explicit values
WEBPORT_PROJECT="myapp" WEBPORT_BRANCH="main" ./scripts/unregister-route.sh
```

## Environment Variables

| Variable | Required | Default | Description |
|----------|----------|---------|-------------|
| `WEBPORT_PROJECT` | No* | git repo basename | Project name |
| `WEBPORT_BRANCH` | No* | current git branch | Branch name |
| `APP_PORT` | Yes (register) | - | Local app port to proxy to |
| `WEBPORT_TTL` | No | 300 | TTL in seconds |
| `WEBPORT_API_PORT` | No | 8080 | Webport API port |
| `WEBPORT_API_URL` | No | http://localhost:8080 | Full webport API URL |

*Auto-detected from git repository if not set.

## Integration Example

Add to your dev server startup:

```bash
#!/usr/bin/env bash
# Start your dev server and register with webport

npm run dev &
DEV_PID=$!

# Register the route
APP_PORT=3000 ./scripts/register-route.sh

# Keep heartbeat running while dev server runs
while kill -0 $DEV_PID 2>/dev/null; do
  sleep 60
  ./scripts/heartbeat-route.sh
done

# Cleanup on exit
./scripts/unregister-route.sh
```
