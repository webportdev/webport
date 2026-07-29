# webport scripts

Use the bootstrap installer for the local-first native installation:

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

With no mode, TLS, or domain flags, the installer selects `full`,
`local-ca`, and `webport.localhost`. Interactive installation asks before
trusting the generated CA. Automated local installation uses:

```bash
./scripts/install.sh --local --trust-local-ca --non-interactive --yes
```

Linux `webport` and `full` installations expose
`webport-stack.target` for starting, stopping, restarting, and inspecting the
two component services together.

Use `--tls-mode local-ca --base-domain webport.localhost` for local HTTPS
without an ACME provider or public DNS. The generated public root certificate
must be added to each client's trust store; on macOS,
`--trust-local-ca` explicitly installs it in the System Keychain. See the main
README.

Traefik accepts any built-in Lego DNS provider code. Cloudflare,
DigitalOcean, and Route53 additionally support interactive credential
collection and wildcard A/AAAA synchronization through `webport-dns`.

The route helper scripts remain useful for legacy direct API integration:

- `register-route.sh`
- `heartbeat-route.sh`
- `unregister-route.sh`

For normal development workflows, prefer:

```bash
webport dev -- npm run dev
```

`webportctl` and `webport-dns` remain compatibility commands.
