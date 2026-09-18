# Configuration-driven development sessions

This document is the version 1 contract for the configuration-driven
`webport dev` session. It is the source of truth for the loader, planner,
supervisor, CLI output, and fixtures. The implementation plan is tracked in
[`agents-context/tasks/configuration-driven-development-sessions.md`](../agents-context/tasks/configuration-driven-development-sessions.md).

## Configuration files and merge

Webport searches from the caller's current directory upward, stopping at the
canonical Git worktree root, for the nearest `.webport.yaml`. A caller outside
Git may use `--config PATH`; an explicit path is also resolved directly and
does not perform the upward search. If the selected primary file is
`DIR/.webport.yaml`, `DIR/.webport.local.yaml` is an optional second input.
For an explicit primary filename, the local filename is still
`DIR/.webport.local.yaml`.

The two YAML documents are parsed independently with duplicate-key detection
and strict key checking. Their node trees are merged in order:

* mappings merge recursively;
* scalars and sequences replace the earlier value; and
* `null` is rejected in version 1 rather than acting as a deletion marker.

An absent local file is normal. An unreadable local file is an error. The
merged document must contain `version: 1`; no environment expansion or
runtime side effect occurs while loading it.

## Version 1 schema

The following keys are accepted. Keys not listed here are errors and errors
include the source file and YAML field path.

```yaml
version: 1                         # required integer
project: myapp                     # optional inferred project name
branch: feature/auth               # optional inferred branch name
worktree_root: /path/to/worktree   # optional non-Git/monorepo override

session:
  env_files: [.env, .env.local]    # optional dotenv files, in listed order
  retain_last_summary: true        # default true
  exports:
    bash: .webport/env              # optional mode-0600 export
    fish: .webport/env.fish
    json: .webport/env.json

requires:
  commands: [go, pnpm]             # optional LookPath preflight list

ports:
  frontend: {first_free: 4000}
  api: {fixed: 8080}
  worker: {random: [10001, 60000]}
  adopted: {discover: true}

env:
  PUBLIC_API: "${routes.api.url}"
  DEV_TOKEN:
    value: literal
    sensitive: true
  SESSION_SECRET:
    generate:
      lifetime: session        # session (default) or project
      length: 32
      sets: [lower, upper, digit]

services:
  api:
    command: [go, run, ./cmd/api]
    working_dir: .
    env: {API_PORT: "${ports.api}"}
    depends_on: []
    completion: process        # process (default) or exit
    ready:
      http:
        url: http://127.0.0.1:${ports.api}/health
        method: GET
        status: [200]
        interval: 500ms
        timeout: 2s
        overall_timeout: 60s
        insecure_skip_verify: false
    endpoints:
      local: http://127.0.0.1:${ports.api}
    logs:
      destination: file        # session (default), none, file, or directory
      path: .webport/api.log
      mode: truncate            # truncate (default) or append
      streams: combined         # combined (default), separate
      max_bytes: 10485760       # optional bounded rotation size
      backups: 2
    shutdown:
      signal: SIGTERM
      grace_period: 5s
      command: [./scripts/stop-api]
      timeout: 30s
    route:
      project: myapp-api
      branch: feature/auth
      port: api
      optional: true
      export: {host: API_HOST, url: API_URL}
    platform: [linux, darwin]

profiles:
  default:
    services: [api]
    env: {PROFILE_NAME: default}
```

### Identity and paths

`project` and `branch` override Git inference independently. `worktree_root`
is allowed only for a non-Git checkout or an explicitly documented monorepo
boundary. Project and branch values use the same validation rules as route
identities. Relative paths are resolved against the primary configuration
directory, including `working_dir`, dotenv files, logs, and exports. Project
secrets use the per-user runtime state directory keyed by the canonical
worktree.

The resolved identity contains the project, branch, repository/worktree roots,
a stable `session.scope`, and a fresh opaque launch `session.id`. The scope is
derived from the project and canonical worktree path using a readable prefix
and a collision-resistant digest. Two paths with the same basename therefore
cannot share a scope. A second launch in the same worktree has the same scope
but a different session ID.

Same-branch linked worktrees retain the raw route identity and do not gain a
hidden hostname discriminator. A public route from such a worktree must use
an explicit `route.project` and/or `route.branch` that makes its identity
unique. If route identities or generated hostnames still collide, preflight
fails; loopback-only services remain usable when no required route is present.

### Environment and interpolation

The effective environment precedence, from lowest to highest, is inherited
environment, configured dotenv files, top-level `env`, selected profile `env`,
then service `env`. Dotenv accepts `NAME=value`, optional single/double
quotes, comments outside quotes, and blank lines; it does not execute shell
syntax. Duplicate dotenv names use the later file's value.

Only these references are expanded, with dependency-graph resolution and
cycle detection:

* `${project}`, `${branch}`, `${session.scope}`;
* `${ports.NAME}`;
* `${env.NAME}`; and
* `${routes.NAME.host}` and `${routes.NAME.url}`.

Expansion is literal and has no shell, command, glob, or escape processing.
Values are tracked with sensitivity metadata. Sensitive values and values
derived from them are redacted in Webport-generated human output, status/config
JSON, errors, runtime metadata, and the retained summary. Webport does not
scrub arbitrary child stdout/stderr before it reaches the terminal or a log
mirror, so applications should avoid printing secrets. Sensitive interpolation
in an argument-array element or shell command is rejected; a child must receive
that value through its environment. `--show-sensitive` may reveal values only
in explicitly interactive `config`/`env` output. Configured export files are a
separate, mode-0600 opt-in artifact that contains the resolved values while the
session is active.

Generated values require a positive `length` and exactly one of `sets` or
`alphabet`. Named sets are `lower`, `upper`, `digit`, `hex`, and `base64url`.
The default lifetime is `session`; `project` values are stored as plaintext
plus policy metadata in a separate mode-0600, worktree-scoped secret file and
may be removed only by `webport dev clean --secrets`. That file is the only
ordinary persistent artifact containing the generated value; session state,
exports after shutdown, plans, and summaries do not contain it. A child that
prints an environment value can still place it in terminal output or a log
mirror.
Generation uses `crypto/rand`.

### Ports, services, readiness, and shutdown

Each port entry contains exactly one of `fixed`, `first_free`, `random`, or
`discover`. `fixed` and `first_free` are integers; `random` is an inclusive
two-integer range. Allocated ports are unique and checked on loopback before
startup. Dynamic allocations are rechecked immediately before their owning
service starts; a first-free or random port is replanned if it lost a race. A
discovered port is selected from the owning process's loopback listener after
launch and becomes available to dependent services, readiness checks,
endpoints, and routes. One discovered port is supported per service.

Each service contains exactly one of `command` (an argument array) or `shell`
(an explicit platform shell string). `completion: process` is the default and
requires the process to remain alive; an unexpected exit, including status
zero, fails the required session. `completion: exit` must finish successfully
before readiness and dependents proceed. Dependencies are named by
`depends_on`, must form an acyclic graph, and are started in deterministic
topological order with independent branches allowed to run concurrently.

`ready` contains exactly one of `tcp`, `http`, or `command`. TCP and HTTP use
per-probe and overall timeouts. HTTP accepts `method`, `status` (one or more
expected codes), `headers`, `interval`, `timeout`, and
`insecure_skip_verify`; TLS verification is on by default. A command check is
an argument array with a bounded per-probe timeout and treats exit status zero
as ready. HTTP also accepts `overall_timeout`. Readiness is cancelled
when its service, dependency, or session fails.

`shutdown.signal` defaults to the signal received by the foreground owner
(`SIGTERM` for a control-requested stop); `grace_period` defaults to 5 seconds.
An exit-completing service's shutdown command is registered once its start
command succeeds, even if readiness later fails. Shutdown commands have an
individual timeout and run in reverse dependency order. There is no automatic
Docker/Podman/Compose interpretation.

### Routes and exports

`route.port` names a configured port and is required. `route.project` defaults
to `${project}-${service}` for non-primary services and `${project}` for the
primary selected service. `route.branch` defaults to `${branch}`. Route
identity is the raw `{project}:{branch}` pair; hostnames are generated only by
`route.BuildDomainChecked`. Duplicate raw identities, duplicate hostnames, and
invalid names fail preflight.

The daemon `/config` response is queried before children start. Required
routes fail preflight if it is unavailable. Optional routes allow local startup
and expose no public host/URL until daemon configuration is available. A route
is registered only after its service is ready, has its own renewable lease, and
is released before external shutdown commands run.
Transient heartbeat failures retry at the heartbeat interval for up to one
route TTL. Missing leases are re-registered; permanent failures or exhaustion
of that recovery window fail required routes.
Configured children carry `WEBPORT_SESSION_MANAGED=1` so daemon process
discovery does not treat their generic `WEBPORT_ROUTE` context as a separate
opt-in or bypass the session's readiness checks.

`route.export` accepts only the aliases `host` and `url`, each mapping to a
valid environment variable name. Aliases are globally unique. They are added
to the resolved environment of the owning service and its transitive
dependents, and are available to `${env.NAME}` interpolation only after route
resolution. An alias cannot overwrite a configured environment name or
another alias; public values are never invented for unavailable optional
routes.

### Profiles and CLI operations

The `profiles` mapping is required to contain `default` and each profile must
select at least one service. A selected service launches its transitive
dependency closure. A named profile launches its listed services and their
closure. An empty profile is invalid.

`webport dev` with no positional operation/service selects `default` and runs
in the foreground. The reserved operations are `check`, `config`, `status`,
`logs`, `env`, `exec`, `stop`, and `clean`; a service cannot use one of these
names. A positional service is interpreted only after checking that it is a
declared service. `--` always selects the legacy wrapper form and everything
after it is passed byte-for-byte to the child, even when a config file exists.
The wrapper keeps its existing route flags and lifecycle.

The session commands are distinct from daemon-level `webport status` and
`webport config`:

* `check` validates the selected dependency closure, environment references,
  generator policies, readiness configuration, and executables without
  starting commands or creating generated values;
* `config` prints the resolved redacted plan, preferring the live session's
  actual ports and values when available. Values contributed by Webport or the
  session configuration are shown by default; `--include-inherited` also
  includes the inherited process environment, and `--show-sensitive`
  explicitly reveals sensitive values in this inspection output;
* `status` prints live state or the retained last-session record;
* `env` renders Bash/POSIX/Zsh, Fish, or JSON environment data. It shows
  Webport-managed values by default; `--include-inherited` restores the full
  resolved environment;
* `logs` reads paths from live or retained session metadata and may follow
  active files;
* `exec` runs a one-off command with the selected service environment;
* `stop` asks the foreground owner to run graceful shutdown; and
* `clean --secrets` explicitly removes project-lifetime secrets.

Human output names the object as a “development session”. JSON output is
versioned with `schema_version: 1` and stable field names. Environment output
uses shell-specific escaping. Live terminal diagnostics may mention a failed
PID; a retained record never stores reusable PIDs, lease IDs, control tokens,
or sensitive values. A no-session query has a stable nonzero exit status.

### Runtime state and artifacts

Per-user live state, the worktree lock, the authenticated control socket, and
the retained last-session record are stored beneath
`$XDG_RUNTIME_DIR/webport` when that variable is available; otherwise Webport
uses the operating system user-cache directory under `webport/runtime`.
Filenames are keyed by a digest of the canonical worktree root. The live file
is removed after orderly shutdown, while the redacted last-session record is
replaced according to `session.retain_last_summary`.

The default `session` log destination writes one combined, mode-`0600` file per
service beneath Webport's worktree-scoped runtime state, not the checkout. It
rotates at 10 MiB with two backups and remains available for the latest retained
session. A new live session removes older managed logs; disabling
`retain_last_summary` removes its managed logs at shutdown. Explicit `none`
keeps terminal-only output. Explicit `file` and `directory` paths are relative
to the primary configuration directory unless an absolute path is configured,
retain their configured stream and rotation behavior, and are never removed by
managed-log cleanup. `path` is not accepted for `session`; select `file` or
`directory` to control the location. Exports are mode `0600` and removed on
shutdown.
Project-lifetime secrets are stored in the per-user runtime directory beside
the hashed worktree state, never under the checkout, and remain only until
`webport dev clean --secrets`. Webport does
not provide detached sessions, infer state from Docker/Podman/Compose, capture
unconfigured runtime-assigned ports, perform continuous health checks, scrub
arbitrary child output for secrets, or clean up after SIGKILL or power loss.

### Generic runtime examples

Webport supervises generic commands; runtime-specific lifecycle remains in
project-owned scripts. Attached runtimes can be used directly:

```bash
webport dev -- docker compose up
webport dev -- podman compose up
webport dev -- docker compose up --abort-on-container-exit
```

For a detached runtime, start it explicitly and make the configured command a
project-owned wait/monitor script. This keeps Webport's foreground ownership
and timeout separate from the runtime's own lifecycle:

```bash
docker compose up -d
webport dev -- ./scripts/wait-for-dev-stack
```

From another terminal, inspect or follow the plain per-service log:

```bash
webport dev status --format json
webport dev logs backend --follow
```

## Deferred from version 1

Detached sessions, `init`, includes, editor schemas, continuous health checks,
automatic restart policies, alternate failure policies, CLI environment
overrides, and provider/framework-specific service types are explicitly
deferred. `discover: true` ports and the `platform` constraint are part of
version 1, with discovery limited to one listener owned by each configured
service and platform values limited to the supported host operating systems.
