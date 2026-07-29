# Migrating to the unified webport workflow

The redesigned release keeps the existing API and helper binaries for one
migration release. New documentation and installations use `webport`.

## Command mapping

| Previous | Preferred |
| --- | --- |
| `webportctl -port 3000` | `webport route --port 3000` |
| `webportctl -query-config` | `webport config` |
| `webport-dns status` | `webport dns status` |
| `webport-dns sync ...` | `webport dns sync ...` |
| `WEBPORT_ROUTE='app:main' npm run dev` | `webport dev -- npm run dev` |
| `/usr/local/bin/webport` service command | `webport daemon` |

The compatibility binaries use the same lease client and automatically
recover after daemon restarts.

## Installation

With no options, installation now selects:

```text
mode: full
TLS: local-ca
domain: webport.localhost
```

Use `--public` for the former ACME default. Existing mode flags remain:

```text
--mode full       managed webport and Traefik
--mode webport    equivalent to --external-traefik
--mode traefik    managed Traefik only
```

Noninteractive local installation requires explicit trust consent:

```bash
--local --trust-local-ca --non-interactive --yes
```

Cloudflare files must define a raw token:

```env
CF_DNS_API_TOKEN=token-value
```

Remove `Bearer `, shell quotes, and surrounding whitespace.

## Routes and API

The legacy `/routes` endpoints remain functional. New clients use `/v1/leases`
so overlapping clients cannot unregister each other and can recover cleanly
after daemon restarts.

Legacy heartbeats with no body now preserve the TTL selected at registration.
The new defaults are a 30-second lease and a 10-second heartbeat.

## Process discovery and privileges

New installations run the daemon under the dedicated `webport` account and
disable global process scanning. This removes the need for the API and
configuration publisher to inspect user processes as root.

Replace environment-only discovery with `webport dev`. It launches the same
command, tags only that process tree, discovers the actual listener as the
calling user, and maintains a lease.

Existing installations that deliberately require system-wide discovery may
temporarily retain the legacy root configuration, but it is deprecated and
should not be used for new projects.

