# webport scripts

Use the bootstrap installer for supported native installation:

```bash
curl -fsSL https://raw.githubusercontent.com/webportdev/webport/main/scripts/bootstrap-install.sh | bash
```

From a checkout:

```bash
./scripts/install.sh        # Linux/systemd
./scripts/install-macos.sh  # macOS/LaunchDaemons
```

Both installers support `webport`, `traefik`, and `full` modes. Managed
Traefik installations download the pinned official upstream binary and verify
its published checksum. No custom proxy build or Docker image is required.

Traefik accepts any built-in Lego DNS provider code. Cloudflare,
DigitalOcean, and Route53 additionally support interactive credential
collection and wildcard A/AAAA synchronization through `webport-dns`.

The route helper scripts remain useful for direct API integration:

- `register-route.sh`
- `heartbeat-route.sh`
- `unregister-route.sh`

For normal development workflows, prefer `webportctl`; it registers a route,
sends heartbeats, and unregisters on shutdown.
