# Feature request: configuration-driven development sessions

Status: implemented in v0.1.0; retained as the design record

## Summary

Add a project configuration file so running `webport dev` from a checkout can
start and supervise the project's complete development environment. The
existing command-wrapper form remains available:

```bash
webport dev -- npm run dev
```

When a configuration file is present, the shorter form starts its default
profile:

```bash
webport dev
```

The configuration should cover reusable development-session plumbing:

- selecting worktree-safe ports;
- building, injecting, saving, and displaying environment variables;
- starting native commands and externally managed resources through the same
  command lifecycle;
- ordering services, one-shot setup tasks, and readiness checks;
- publishing zero or more services through Webport;
- mirroring prefixed output to the terminal and per-service log files;
- supervising the process tree and cleaning it up reliably; and
- exposing session status, URLs, settings, logs, and an environment that other
  developer tools can consume.

Projects should still provide application-specific commands such as database
migrations or seed-user creation. Webport should orchestrate those commands,
not learn their domain logic.

## Motivation

The common one-server case is already pleasantly small:

```bash
webport dev -- npm run dev
```

Real projects often need more than one server. They accumulate shell launchers
which independently solve the same problems: finding ports, starting
containers, generating local credentials, exporting URLs, waiting for
readiness, running setup steps, teeing logs, handling signals, and registering
several routes. Those scripts are difficult to make portable between Linux and
macOS and easy to get subtly wrong around quoting, cleanup, port conflicts, and
concurrent worktrees.

The Cortex `scripts/devwithdeps.sh` launcher is a useful example. It is roughly
400 lines, even though most of its behavior is not specific to Cortex.

### Reusable behavior found in Cortex

| Existing launcher behavior                                 | General Webport capability                           |
| ---------------------------------------------------------- | ---------------------------------------------------- |
| Takes a non-blocking worktree lock                         | One active session per worktree                      |
| Checks required executables                                | Declarative preflight requirements                   |
| Selects six random ports and the first free frontend port  | Named fixed, random, and first-free ports            |
| Generates MinIO and superadmin credentials                 | Session-scoped generated values marked sensitive     |
| Builds database, backend, frontend, SMTP, and storage URLs | Variables interpolated from named ports and routes   |
| Writes mode-`0600` `.devenv` and `.devenv.fish` atomically | Shell-neutral session state with export formats      |
| Starts PostgreSQL, MinIO, and Mailpit with Compose         | Generic start, monitor, logs, and shutdown commands  |
| Waits for PostgreSQL and HTTP health endpoints             | Command, TCP, and HTTP readiness checks              |
| Runs migrations, creates a user, and creates a bucket      | Dependency-aware one-shot setup tasks                |
| Starts Air and Vite                                        | Supervised native processes with working directories |
| Publishes Vite and an already-running Mailpit              | Multiple named routes in one session                 |
| Supplies the route hostname to Vite before it starts       | Precomputed route host and URL variables             |
| Tees frontend/backend output into separate files           | Terminal output plus per-service log mirrors         |
| Stops all children if one exits unexpectedly               | Configurable failure and shutdown policies           |
| Stops containers but preserves bind-mounted data           | Explicit shutdown commands chosen by the project     |
| Prints endpoints, credential locations, and log locations  | A standard startup summary and inspect command       |

The Cortex-specific parts are only the actual migration, user creation, bucket
creation, and application commands. Those fit naturally as declarative
one-shot tasks and processes.

### Webport integration friction found in Cortex

The launcher currently has to do several things which Webport already knows or
could expose more directly:

- probe `webport config` to decide whether routing is available, then parse its
  JSON with `sed`;
- detect the Git branch and duplicate Webport's branch-to-hostname slugging;
- compute public hostnames before Vite starts so Vite can allow exactly that
  host;
- wrap Vite with `webport dev` while separately keeping a `webport route`
  process alive for Mailpit; and
- copy Webport identity, host, and URL values into the session environment for
  downstream scripts.

Even before multi-service sessions are implemented, the existing wrapper can
improve this workflow by injecting stable generic values into its child:

```text
WEBPORT_PROJECT
WEBPORT_BRANCH
WEBPORT_ROUTE
WEBPORT_HOST
WEBPORT_URL
```

The host and URL do not depend on listener discovery, so Webport can resolve
them before launching Vite. A stable `--format json` and `--format env` on
configuration/route-resolution commands would also remove ad hoc output
parsing. Manifest `route.export` mappings can provide project-specific aliases
such as `WEBPORT_DOMAIN` and `WEBPORT_PUBLIC_URL`.

## Design goals

1. Make the easy case `webport dev`, while preserving
   `webport dev -- COMMAND` exactly as a zero-configuration escape hatch.
2. Work consistently in normal checkouts, Git worktrees, monorepos, Linux, and
   macOS.
3. Keep the committed configuration useful to every developer while allowing
   ignored local overrides.
4. Make startup deterministic and observable: resolve the plan first, print
   what will run, then start it.
5. Treat secrets and generated credentials carefully. They must be redacted in
   summaries and must not be placed in command-line arguments by Webport.
6. Treat every runtime as commands. Webport supplies orchestration and
   observability without interpreting Docker, Podman, Compose, or application
   internals.
7. Preserve Webport's current route semantics, renewable leases, deterministic
   Traefik output, and route cleanup guarantees.
8. Fail with an actionable service name, check, and log tail instead of leaving
   a partially started environment behind.

## Non-goals

- Reimplementing Docker Compose, Mise, Procfile runners, or full CI workflow
  engines.
- Encoding database migrations, fixtures, buckets, or framework-specific
  behavior in Webport.
- Managing production deployments.
- Persisting application secrets in the Webport daemon.
- Parsing generated hostnames to recover route identities.
- Automatically deleting volumes, bind-mounted data, or other persistent
  project state.
- Requiring every process to be public. Most dependencies should remain
  loopback-only and un-routed.

## Proposed project files

Search upward from the current directory to the Git/worktree root for:

```text
.webport.yaml
```

`.webport.local.yaml`, when present beside it, is merged afterward and should
normally be ignored by Git. Mappings merge recursively; scalars and arrays
replace the earlier value. The merged result is validated as one document.
`--config PATH` selects an explicit primary file and uses a same-directory
`.webport.local.yaml` when present.

The first version should use a strict, versioned schema and reject unknown
keys. A typo in a port or secret setting should never silently change startup
behavior.

```yaml
version: 1
```

YAML fits the nested service graph and is already familiar to developers using
Compose and common CI systems. A future JSON representation could be accepted
without changing the data model, but supporting several hand-written formats
in the first release would increase documentation and validation work.

## Proposed CLI

```text
# Start the default profile from .webport.yaml in the foreground
webport dev

# Start a profile or a selected service plus its dependency closure
webport dev --profile full
webport dev frontend

# Preserve the existing unconfigured wrapper
webport dev -- npm run dev

# Validate or inspect without starting anything
webport dev check
webport dev config
webport dev config --show-sensitive

# Inspect a session from another terminal
webport dev status
webport dev logs
webport dev logs frontend --follow
webport dev env --shell bash --show-sensitive
webport dev env --shell fish --show-sensitive
webport dev env --format json
webport dev exec backend -- go test ./...

# Ask the foreground supervisor to stop gracefully
webport dev stop
```

`webport status` should continue to describe the system daemon and route
publication. `webport dev status` describes the project session. Output should
make that distinction explicit.

Foreground supervision is enough for the first version. A detached
`webport dev --detach` mode is useful later, but requires a durable per-user
supervisor protocol and should not be approximated with an orphaned child.

Version 1 does not append to or replace commands from `.webport.yaml` and does
not provide CLI environment overrides for configured sessions. Advanced
configuration belongs in the committed or local configuration file. The
existing `webport dev [route options] -- COMMAND` wrapper retains its current
flags and passthrough behavior.

## Draft configuration

This abridged example models the reusable parts of the Cortex launcher. Names
and exact syntax are intentionally provisional; the important part is the
capability model.

```yaml
version: 1

project: cortex # default: inferred Git repository name

session:
  env_files:
    bash: .devenv
    fish: .devenv.fish
  retain_last_summary: true

requires:
  commands:
    - docker
    - go
    - air
    - pnpm

ports:
  postgres: { random: [10001, 60000] }
  minio: { random: [10001, 60000] }
  minio_console: { random: [10001, 60000] }
  backend: { random: [10001, 60000] }
  mailpit_smtp: { random: [10001, 60000] }
  mailpit_http: { random: [10001, 60000] }
  frontend: { first_free: 4000 }

env:
  # A stable, worktree-specific identifier suitable for container names and
  # Compose's project namespace.
  COMPOSE_PROJECT_NAME: "${session.scope}"

  POSTGRES_USER: postgres
  POSTGRES_PASSWORD:
    value: postgres
    sensitive: true
  POSTGRES_DB: synced_db
  POSTGRES_PORT: "${ports.postgres}"
  DATABASE_URL:
    value: "postgres://postgres:${env.POSTGRES_PASSWORD}@localhost:${ports.postgres}/synced_db?sslmode=disable"
    sensitive: true

  MINIO_PORT: "${ports.minio}"
  MINIO_CONSOLE_PORT: "${ports.minio_console}"
  MINIO_ACCESS_KEY:
    generate: { length: 20, sets: [lower, upper, digit] }
    sensitive: true
  MINIO_SECRET_KEY:
    generate: { length: 40, sets: [lower, upper, digit] }
    sensitive: true
  MINIO_ROOT_USER: "${env.MINIO_ACCESS_KEY}"
  MINIO_ROOT_PASSWORD: "${env.MINIO_SECRET_KEY}"

  SUPERADMIN_USERNAME: devsuperadmin
  SUPERADMIN_EMAIL: devsuperadmin@localhost
  SUPERADMIN_PASSWORD:
    generate: { length: 20, sets: [lower, upper, digit] }
    sensitive: true

  BACKEND_PORT: "${ports.backend}"
  FRONTEND_PORT: "${ports.frontend}"
  MAILPIT_SMTP_PORT: "${ports.mailpit_smtp}"
  MAILPIT_HTTP_PORT: "${ports.mailpit_http}"
  BACKEND_URL: "http://localhost:${ports.backend}"
  FRONTEND_URL: "http://localhost:${ports.frontend}"
  MAILPIT_URL: "http://localhost:${ports.mailpit_http}"
  GRAPHQL_URL: "${env.BACKEND_URL}/query"

services:
  postgres:
    command: [docker, compose, up, --detach, postgres]
    completion: exit
    ready:
      command: [docker, compose, exec, -T, postgres, pg_isready, -U, postgres, -d, synced_db]
      timeout: 60s
    shutdown:
      command: [docker, compose, stop, postgres]
      timeout: 30s

  minio:
    command: [docker, compose, up, --detach, minio]
    completion: exit
    ready:
      http: "http://localhost:${ports.minio}/minio/health/live"
      timeout: 60s
    shutdown:
      command: [docker, compose, stop, minio]
      timeout: 30s

  mailpit:
    command: [docker, compose, up, --detach, mailpit]
    completion: exit
    ready:
      http: "${env.MAILPIT_URL}/readyz"
      timeout: 60s
    shutdown:
      command: [docker, compose, stop, mailpit]
      timeout: 30s
    route:
      project: "${project}-mailpit"
      port: "${ports.mailpit_http}"
      optional: true
      export:
        host: WEBPORT_MAILPIT_DOMAIN
        url: WEBPORT_MAILPIT_URL

  migrate:
    # This small project command reads DATABASE_URL from its environment so
    # the sensitive value does not appear in the process list.
    command: [./scripts/migrate-dev]
    completion: exit
    depends_on: [postgres]

  create-dev-user:
    command: [./scripts/create-dev-user]
    completion: exit
    depends_on: [migrate]

  create-bucket:
    command: [./scripts/create-dev-bucket]
    completion: exit
    depends_on: [minio]

  backend:
    command: [air, serve]
    completion: process
    depends_on: [migrate, create-dev-user, create-bucket]
    log:
      file: .backend_logs
      mode: truncate
    ready:
      http: "http://localhost:${ports.backend}/health"
      timeout: 60s

  frontend:
    command: [pnpm, -C, frontend, run, dev, --host, --port, "${ports.frontend}"]
    completion: process
    depends_on: [backend]
    log:
      file: .frontend_logs
      mode: truncate
    ready:
      http: "http://localhost:${ports.frontend}"
      timeout: 60s
    route:
      project: "${project}"
      port: "${ports.frontend}"
      optional: true
      export:
        # Computed and injected before Vite starts, so it can enforce
        # allowedHosts even though the lease activates after readiness.
        host: WEBPORT_DOMAIN
        url: WEBPORT_PUBLIC_URL

profiles:
  default: [postgres, minio, mailpit, backend, frontend]
  app-only: [backend, frontend]
  backend-only: [backend]
```

An `optional` route means the service still starts with its localhost URL when
the Webport daemon is unavailable. A required route fails during preflight.
Dependency closure means the default profile does not need to repeat the
one-shot tasks on which its applications depend.

Application-specific secrets in commands deserve special handling. The final
schema should let a command reference a sensitive value through its environment
rather than interpolate it into `argv`, where it can appear in process listings.
The example's `create-dev-user` helper intentionally consumes environment
variables for that reason.

## Configuration model

### Identity and worktrees

- Infer `project` and `branch` using the existing Git detection code.
- Use the unambiguous raw `{project}:{branch}` route identity internally.
- Generate hostnames through the existing route-domain implementation; never
  duplicate slugging in the session runner.
- Key a session by the canonical worktree root. A second `webport dev` in the
  same worktree reports the running session and its state-file path.
- Derive a stable, collision-resistant `${session.scope}` from the project and
  canonical worktree path. Configurations can use it for Compose project names,
  container names, PID files, or any other external resource namespace.
- Allow explicit identity overrides for non-Git directories and unusual
  monorepos.

### Ports and endpoints

Named ports should support:

- `fixed: 5173`;
- `first_free: 4000`;
- `random: [10001, 60000]`; and
- `discover: true` for a process which chooses its own listener.

Every selected port is unique within the resolved plan and checked against the
host before launch. Startup should retry allocation when a port loses a race
before the owning service starts, within a bounded limit. Version 1 passes the
selected port to commands through interpolation or environment. Discovering a
runtime-assigned Docker, Podman, or custom-tool port would require parsing or
capturing tool-specific output and is deliberately deferred.

Each service may expose named local endpoints for the startup summary even if
it has no Webport route. Public route hostnames and URLs can be computed after
querying `/config` but before child processes start, allowing Vite and similar
servers to receive a narrow allowed-host value.

### Environment and generated values

Environment precedence should be documented and inspectable. A reasonable
order, lowest to highest, is:

1. inherited process environment;
2. project dotenv files explicitly named in configuration;
3. top-level `env`;
4. profile environment;
5. service environment.

Interpolation should be deliberately small and non-shell-like:

```text
${project}
${branch}
${session.scope}
${ports.NAME}
${env.NAME}
${routes.NAME.host}
${routes.NAME.url}
```

No command substitution, glob expansion, or arbitrary expressions should occur
during interpolation. Cycles and references to unknown values are validation
errors.

Generated values have an explicit lifetime:

- `session`: the default, generated once per launch and removed with live
  session state; and
- `project`: stored in a separate user-only secrets file and reused across
  launches of this worktree.

Both lifetimes are useful in version 1: disposable credentials should use
`session`, while a development signing key or credentials tied to persistent
data may need `project`. Removing project-lifetime values requires an explicit
command such as `webport dev clean --secrets`; clean session shutdown never
deletes them.

Generation uses a cryptographically secure random source and requires a
positive length. A value may select predefined sets such as `lower`, `upper`,
`digit`, `hex`, and `base64url`, or provide one explicit allowed-character
string, but not both. The resolved configuration reports length and character
policy without ever printing the generated value.

Live control state belongs in the per-user runtime directory, keyed by the
canonical worktree path. It includes the supervisor/control endpoint, selected
ports, service state, and route leases and is removed on clean shutdown.
Generated session values should remain in supervisor memory unless a configured
environment export requires them on disk.

Configured Bash, Zsh/POSIX, Fish, and JSON environment exports are opt-in,
mode-`0600`, atomically replaced renderings of the live environment. They are
removed on clean shutdown by default so another tool cannot accidentally source
stale ports or credentials.

Webport retains one redacted last-session summary in the per-user state
directory. It may contain start/stop times, selected profile, final service
states, the process or monitor whose exit ended the session, exit status or
signal, and log paths. It must not contain secret values, reusable process IDs,
control tokens, or lease IDs. Logs follow their configured retention policy and
normally survive shutdown.

Sensitive values are shown as `<redacted>` by default in summaries, `status`,
`config`, and diagnostics. Printing them to the terminal requires an explicit
`--show-sensitive`; Webport must never log their values.

### Command lifecycle and dependencies

All services use one command model. Webport does not have native-process,
Docker, Podman, Compose, or task service types. The only required lifecycle
distinction is what successful execution means:

- `completion: process` means the command must remain alive. Webport supervises
  its process group and treats an unexpected exit as the service stopping. This
  is the default.
- `completion: exit` means the command initializes an external resource or
  completes a one-shot task and must exit successfully. A readiness check may
  then verify the resource. An optional `shutdown.command` releases it.

A migration is `completion: exit` without shutdown. `docker compose up -d` is
`completion: exit` with readiness and shutdown. Air, Vite, an attached
`docker compose up`, and a container wait/log follower are all
`completion: process`.

Commands should be expressed as argument arrays by default. An explicit
`shell:` form can support pipelines and existing short shell fragments, but
its quoting and platform dependence should be visible in the configuration.
Every service can set `working_dir`, environment, dependencies, readiness, log
mirroring, a shutdown policy, and platform constraints. A process-completing
service configures its signal and grace period; an exit-completing service may
configure a shutdown command and timeout. Version 1 does not alter a configured
command through CLI arguments or environment overrides.

The dependency graph must be acyclic. A service starts only after dependencies
are ready or successful. Independent branches can start concurrently.
Exit-completing commands run once per session.

Long-running process groups receive the original interrupt/termination signal,
get a configurable bounded grace period, and are then force-killed if necessary.
This reuses and generalizes the process-group cleanup already implemented by
`webport dev`. The grace period must be long enough for an attached container
or Compose CLI to finish its own resource shutdown.

After an exit-completing start command succeeds, its configured shutdown
command is registered unconditionally. It runs during clean shutdown, startup
rollback, `webport dev stop`, or failure of another required service, in reverse
dependency order. Webport does not try to infer whether the start command
created, restarted, or adopted an external resource. A shared resource opts out
of session cleanup by omitting `shutdown`.

The foreground supervisor remains active while it owns a running process, a
route lease, or at least one registered shutdown command. A plan containing
only successful one-shot commands with none of those responsibilities exits
when the commands finish.

Every shutdown command has a Webport execution timeout. A nonzero exit or
timeout is reported prominently and makes an otherwise successful shutdown
fail. When cleanup follows an earlier service failure, Webport preserves the
original service failure as the primary result and reports cleanup errors
alongside it. If shutdown is expected to tolerate an already-absent resource,
the configured tool command or an explicit shell wrapper must implement that
idempotence.

By default, an unexpected required process exit stops the session. The retained
last-session record and terminal message include the service name, process ID,
exit code or signal, time, and log path. Exits caused after Webport has begun an
intentional shutdown are recorded as cleanup, not misreported as the initiating
failure.

Optional later policies include `restart: on-failure`, optional services, and
`on_service_exit: keep-running`. They should not complicate the safe default.

### Readiness and health

First-version checks should include:

- TCP connect;
- HTTP(S), with configurable method, expected status, headers, interval, and
  timeout; and
- an argument-array command whose zero exit status means ready.

Tool-native health can be used through a configured command check. Checks need
bounded probe timeouts as well as an overall timeout. When a service fails to
become ready, Webport should show the failed check and a short tail from output
it captured, then run the shutdown commands registered by this session.

Continuous health monitoring and auto-restart are valuable later. Startup
readiness plus process-exit supervision covers the primary use case first.

### External resources and container tools

The generic command model can operate Docker, Podman, either Compose
implementation, a local VM manager, or a project script without Webport linking
their libraries or parsing their state.

Docker Compose supports both models directly. Attached `docker compose up`
aggregates container output and stops its containers when it receives SIGINT or
SIGTERM. Detached `docker compose up -d` exits while leaving containers running,
so it needs an explicit shutdown command. `docker compose stop` stops containers
without removing them, while a project may deliberately configure `down` if it
wants removal. See the official documentation for
[`compose up`](https://docs.docker.com/reference/cli/docker/compose/up/) and
[`compose stop`](https://docs.docker.com/reference/cli/docker/compose/stop/).

An attached configuration lets Compose own signal forwarding and combined
logs:

```yaml
services:
  infrastructure:
    command: [docker, compose, up, postgres, minio, mailpit]
    completion: process
    shutdown:
      signal: inherit
      timeout: 30s
```

A detached configuration makes each lifecycle operation explicit:

```yaml
services:
  postgres:
    command: [docker, compose, up, --detach, postgres]
    completion: exit
    ready:
      tcp: "127.0.0.1:${ports.postgres}"
    shutdown:
      command: [docker, compose, stop, postgres]
      timeout: 30s
```

Direct Docker and Podman containers use the same shape:

```yaml
services:
  redis:
    command:
      [docker, run, --detach, --name, "${session.scope}-redis",
       --publish, "127.0.0.1:${ports.redis}:6379", redis:8]
    completion: exit
    ready:
      tcp: "127.0.0.1:${ports.redis}"
    shutdown:
      command: [docker, container, stop, "${session.scope}-redis"]

  mailpit:
    command:
      [podman, run, --detach, --name, "${session.scope}-mailpit",
       --publish, "127.0.0.1:${ports.mailpit_http}:8025",
       docker.io/axllent/mailpit:v1.30.7]
    completion: exit
    ready:
      http: "http://127.0.0.1:${ports.mailpit_http}/readyz"
    shutdown:
      command: [podman, stop, "${session.scope}-mailpit"]
```

Both
[`docker container stop`](https://docs.docker.com/reference/cli/docker/container/stop/)
and [`podman stop`](https://docs.podman.io/en/latest/markdown/podman-stop.1.html)
send a graceful signal and force-kill after their own timeout. That tool-level
timeout is distinct from the Webport timeout around the shutdown command. If
Webport's timeout is shorter, Webport may terminate the CLI before the external
resource has stopped, which is reported as incomplete cleanup.

Detached resources are not represented by the short-lived start process.
Projects which need unexpected-exit supervision can configure the tool's wait
operation as another required process service:

```yaml
services:
  postgres-lifetime:
    command: [docker, compose, wait, postgres]
    completion: process
    depends_on: [postgres]
```

Docker provides
[`docker compose wait`](https://docs.docker.com/reference/cli/docker/compose/wait/)
and [`docker container wait`](https://docs.docker.com/reference/cli/docker/container/wait/);
Podman provides
[`podman wait`](https://docs.podman.io/en/latest/markdown/podman-wait.1.html).
The monitor is deliberately an ordinary process: if it exits unexpectedly, the
normal required-service rule stops the session and records which monitor ended.
If no wait/monitor command is configured, Webport knows only initial readiness
and cannot detect that the detached resource later exited.

Likewise, attached container/Compose output is captured automatically, but a
detached runtime's logs are not. A project can run `docker logs --follow`,
`podman logs --follow`, or a Compose log follower as another configured process
and mirror that output normally. Webport does not synthesize provider-specific
log commands.

This generality has intentional limits:

- Start, shutdown, wait, and log command syntax remains the responsibility of
  the configuration author.
- Webport does not discover arbitrary runtime-assigned ports in version 1;
  configured commands receive Webport's selected named ports.
- Webport does not capture a start command's stdout as a container ID in
  version 1. Use a deterministic `${session.scope}` name, a runtime-supported ID
  file, or a small project wrapper.
- Webport cannot prove ownership of a resource. A successful start command with
  `shutdown` means the project has authorized that shutdown command, even if the
  resource already existed.
- If a start command creates something and then exits unsuccessfully, its
  shutdown command is not registered. Start commands should be atomic or
  idempotent, and failures may require the tool's own recovery procedure.
- Shutdown commands cannot run if Webport itself is force-killed with SIGKILL,
  the machine loses power, or the process otherwise cannot execute cleanup.
  Route leases still expire, but detached external resources may remain and
  must be handled by their configured tool.
- A detached resource is not continuously supervised unless the configuration
  includes a required wait/monitor process. A startup readiness check alone is
  not an ongoing health guarantee.
- When one attached command represents several resources, Webport sees only
  that command. Tool-specific flags decide whether an individual resource exit
  also terminates the attached command.
- Session status reports the configured command, last readiness result, and any
  required monitor state. It does not synthesize Docker/Podman container state,
  health, restart counts, or exit codes that the configured commands do not
  expose.
- `podman compose` delegates to an external Compose provider, so its precise
  supported flags and behavior depend on the installed provider. Webport passes
  the configured command through unchanged. See the
  [Podman Compose documentation](https://docs.podman.io/en/latest/markdown/podman-compose.1.html).
- Destructive operations such as Compose `down --volumes`, container removal,
  pruning, or bind-mount deletion are never inferred. They occur only when the
  project explicitly places them in a command.

### Routes

A session may publish several services, such as a frontend, API, component
storybook, Mailpit UI, or MinIO console. Each route declares its actual project
and branch identity plus a port. Configuration can derive route project names
from the session project and service name:

```text
frontend -> cortex:{branch}
mailpit  -> cortex-mailpit:{branch}
```

The resolved plan must detect identity and hostname conflicts before starting
applications. All routes use independent renewable leases, and clean shutdown
releases them after services stop accepting traffic. If the daemon restarts,
the active session re-registers every route.

Host routing is sufficient initially. Path-based routing, middleware, custom
headers, and arbitrary Traefik snippets should be considered separately because
they expand both the API and security model.

### Logs and terminal output

Default output should remain useful interactively:

```text
[postgres] ready on 127.0.0.1:43127
[backend ] listening on http://localhost:48712
[frontend] https://cortex-feature-auth.webport.localhost
```

For concurrent services, prefix and color terminal lines by service while
preserving plain file logs. Per-service log settings should support:

- no file, a file path, or a session log directory;
- truncate or append on startup;
- separate or combined stdout/stderr; and
- an optional bounded rotation policy.

`webport dev logs [SERVICE] --follow` should read the session metadata rather
than requiring developers and automation to know filenames. Webport should not
attempt content-based secret scrubbing, which is unreliable; it should prevent
its own sensitive values from being printed and document that child processes
remain responsible for their output.

### Summary, status, and tooling integration

Once startup completes, print a stable summary containing:

- local and public URLs;
- service state and selected ports;
- paths to live runtime metadata, environment exports, and logs;
- the names, but not values, of generated sensitive settings; and
- a concise hint for `status`, `logs`, `env`, and shutdown.

Human-readable output is the default, with `--format json` for editors, test
runners, and agents. The JSON status should include session ID, worktree,
profile, supervised process identifiers, readiness, endpoints, route lease
state, start time, and last error. Sensitive values must remain redacted.

`webport dev exec SERVICE -- COMMAND` should run a one-off command in the
selected service's working directory and resolved environment. It does not
need to enter a container in the first version; Compose users can still run
`docker compose exec` explicitly.

## Other useful workflows this unlocks

### A frontend-only project

One command, one inferred listener, and one public route. A minimal config only
needs the dev command; it can omit ports, dependencies, state exports, and logs.

### A frontend proxying to a backend

Allocate both ports, expose their local URLs to both processes, but publish only
the frontend. Inject the computed public hostname before Vite starts so its
allowed-host policy remains narrow.

### Several developer-facing UIs

Publish the application, Storybook, Mailpit, an API documentation server, or an
object-store console under separate route identities while keeping databases
and SMTP ports private.

### A containerized dependency stack with native hot reload

Use configured Docker, Podman, Compose, or project commands for PostgreSQL,
Redis, queues, and object storage; use native commands for faster framework
reloaders. Readiness links them without a bespoke shell polling loop.

### Monorepo profiles

Start only a package and its dependency closure, or define `frontend`,
`backend`, and `full` profiles. Per-service working directories avoid wrapper
scripts whose only purpose is `cd package && command`.

### Concurrent Git worktrees

Give every worktree distinct ports, Compose project names, route hostnames,
state files, and logs. The current branch naturally participates in route
identity while the path keeps same-branch worktrees isolated locally.

### Editor, test, and agent integration

Tools can call `webport dev status --format json` or source a generated env file
immediately before use. This removes duplicated knowledge of random ports and
keeps runtime settings discoverable in one place.

## Additional features to consider after the core

- `webport init` to detect common `package.json`, Compose, Procfile, and Mise
  tasks and generate a small reviewed starter file.
- An `include` mechanism for monorepos, provided includes cannot escape the
  repository without an explicit opt-in.
- OS/architecture conditions for platform-specific commands.
- File-watch-triggered one-shot tasks such as code generation, while leaving
  application hot reload to the application tool.
- Example or generated snippets for common Docker, Podman, and Compose
  lifecycles. They must expand into ordinary visible commands rather than add a
  provider abstraction or hide mutable infrastructure behavior.
- Optional desktop notifications when a long startup becomes ready or fails.
- An opt-in detached session manager once foreground lifecycle semantics are
  mature.

## Recommended delivery phases

Phases 1 through 3 are implementation slices for version 1. Phase 4 is
follow-up ergonomics and does not block the configuration-driven session
release.

### Phase 1: one configured process

- Discover and strictly validate `.webport.yaml`, merge an automatic
  `.webport.local.yaml`, and validate the resolved document.
- Preserve `webport dev -- COMMAND` compatibility.
- Inject the resolved route identity, host, and URL into configured and
  zero-configuration child processes; add stable JSON/env resolution output.
- Run one configured native command with working directory and environment.
- Support fixed or first-free named ports, one readiness check, one route, and
  existing process-group cleanup.
- Print resolved URLs and provide redacted `webport dev config`.

This immediately reduces the most common project command to `webport dev`.

### Phase 2: session supervisor

- Add a dependency DAG, concurrent independent startup, one-shot tasks, and
  multiple routes.
- Add `process` and `exit` completion, explicit shutdown commands, and required
  monitor processes for tools which offer them.
- Add random ports, configurable session/project generated values, live runtime
  state, a redacted last-session record, Bash/Fish/JSON exports, locks, status,
  log mirrors, and actionable startup failure output.
- Add profiles and selected-service startup.

This phase replaces most bespoke multi-process shell launchers.

### Phase 3: richer operations

- Add `logs`, `exec`, `stop`, and bounded log rotation.
- Add generated examples for attached and detached Docker, Podman, and Compose
  commands without adding runtime-specific service types.
- Add optional restart/failure policies after the default lifecycle has proven
  reliable.

### Phase 4: ergonomics

- Add `webport init`, reusable expanded templates, monorepo includes, and
  editor-friendly JSON schemas/completion.
- Evaluate detached sessions based on real foreground-session usage.

## Acceptance criteria for replacing Cortex's launcher

The design is successful when Cortex can replace `scripts/devwithdeps.sh` with
a committed Webport configuration plus only small Cortex-specific setup
commands, while preserving these properties:

1. `webport dev` starts PostgreSQL, MinIO, Mailpit, migrations, bootstrap tasks,
   Air, and Vite in dependency order.
2. Two different worktrees can run simultaneously without sharing ports,
   routes, locks, Compose resources, state, or logs.
3. Vite receives its exact public hostname before launch; the frontend and
   Mailpit routes are renewed and recover after a daemon restart.
4. Runtime ports, URLs, and credentials are queryable from the active
   supervisor and optionally available through configured mode-`0600` Bash,
   Fish, or JSON exports.
5. Startup does not report ready until dependencies and applications pass
   their configured checks.
6. Backend and frontend output remains visible in the terminal and mirrored to
   their current log paths.
7. A failed required process identifies the service, preserves a useful log
   tail, stops the rest of the session, releases routes, and returns a nonzero
   status.
8. Ctrl+C terminates complete process groups, runs every registered shutdown
   command in reverse dependency order, removes live state and exports, retains
   a redacted final record, and preserves PostgreSQL and MinIO data because the
   configured commands do not delete it.
9. Generated secrets are absent from Webport-generated terminal diagnostics,
   route status, and process arguments. Child stdout/stderr is mirrored as-is
   and is outside Webport's content-based secret-redaction guarantee.
10. Projects which do not adopt a configuration file see no behavior change in
    `webport dev -- COMMAND`.

## Architecture decisions

1. The project file is `.webport.yaml`.
2. `.webport.local.yaml` is loaded automatically as the ignored local override.
3. Bash, Fish, and JSON environment files are opt-in with configurable names;
   the active environment is queryable through the CLI without them.
4. Docker, Podman, Compose, and other resource managers receive no special
   service type. They use the same command lifecycle as native processes.
5. A shutdown command is never inferred. Once a configured exit-completing
   start succeeds, its shutdown command always runs when the foreground session
   ends. Shared resources omit shutdown.
6. Route availability is required unless `optional: true` is configured.
7. A non-primary route derives `project-service` by default.
8. Version 1 supports cryptographically generated secrets with configurable
   length and allowed character sets, using explicit session or project
   lifetime.
9. Clean shutdown deletes live control state and environment exports, retains
   logs according to their policy, and retains one redacted last-session record.
10. Version 1 does not append to or replace configured commands and does not
    apply CLI environment overrides. Advanced session configuration is file
    based; the existing zero-configuration wrapper keeps its current arguments.
11. Any required process exiting unexpectedly ends the foreground session,
    even with status zero. Webport records which process exited and its status
    or signal before beginning cleanup.
