# Development tasks: configuration-driven development sessions

This document breaks `agents-context/feature-request.md` into an ordered implementation plan. It covers the version 1 release described by delivery phases 1 through 3. Phase 4 ideas are recorded as deferred work and must not delay the initial release.

No implementation code is included here.

## Implementation progress

This ledger is updated and committed after each step so a later session can
resume without reconstructing completed work from the repository diff.

| Step | Status | Completed work / handoff |
| --- | --- | --- |
| 1 | complete | Version 1 contract frozen in `docs/configuration-driven-development-sessions.md`. |
| 2 | complete | Added stream-aware CLI dispatch, canonical Git worktree detection, injectable session primitives, and `internal/devsession/daemon` with `/config`, lease, heartbeat/recovery, release, and legacy-route compatibility. Focused tests pass. |
| 3 | complete | Added strict, side-effect-free YAML loading under `internal/devsession/config`: upward/worktree-bounded discovery, explicit config selection, primary/local tree merge, duplicate/unknown-key/type/duration/version diagnostics, and typed unresolved configuration. Focused tests pass. |
| 4–22 | pending | Not started. |

## Target outcome

After these tasks are complete, a project can commit `.webport.yaml` and run `webport dev` to start a foreground, worktree-scoped development session. Webport will resolve ports, environment, routes, dependencies, readiness, logs, and cleanup before supervising the session. Existing users of `webport dev [route options] -- COMMAND` must retain the same command behavior, with the addition of stable Webport identity/URL environment variables.

The implementation must preserve these invariants:

- Route identity remains the raw `{project}:{branch}` pair; generated hostnames are never parsed to recover identity.
- Every configured command is passed through as a generic command. There are no Docker-, Podman-, Compose-, or framework-specific service types.
- Sensitive values never appear in Webport-generated logs, normal terminal output, status/config output, state summaries, or child argument vectors.
- Required process failure triggers bounded cleanup, reverse-dependency shutdown, route release, and a nonzero result.
- The active session is foreground-owned. Version 1 must not simulate detach by orphaning processes.
- Runtime state and exports are user-only, atomically written, and removed after clean shutdown; logs and a redacted last-session summary follow their retention rules.
- Linux and macOS use the same session semantics, with platform-specific process details isolated behind build-tagged files where necessary.

## Recommended package boundaries

Keep `cmd/webport` responsible for argument parsing, terminal wiring, and exit behavior. Put independently testable session behavior under `internal/devsession` (or a small set of packages beneath it):

```text
internal/devsession/
  config/          # YAML schema, discovery, merge, strict decoding, validation
  plan/            # identity, dependency closure, interpolation, ports, routes
  command/         # process groups, output capture, readiness, shutdown commands
  state/           # worktree lock, runtime metadata, control endpoint, exports
  supervisor/      # startup scheduler, route leases, failure and cleanup policy
```

This is a suggested decomposition, not a requirement to create five Go packages immediately. Avoid a single large `cmd/webport/cli.go` implementation, and avoid circular dependencies between configuration, planning, and runtime state.

## Sequential implementation tasks

### 1. Freeze the version 1 contract

Convert the provisional examples into a written, testable contract before defining Go types.

Work:

- Specify every accepted YAML key and value type, including:
  - project/branch/worktree identity overrides;
  - `session`, `requires`, `ports`, dotenv inputs, top-level/profile/service environment;
  - generated-value character policy and `session` versus `project` lifetime;
  - services, argument-array commands, explicit shell commands, working directories, dependencies, completion mode, readiness, endpoints, logs, shutdown, and routes;
  - profiles and the default-profile rule.
- Define defaults and mutual exclusions. Examples include `completion: process`, route project naming, route branch inheritance, readiness timeouts, log mode, shutdown signal/grace period, and exactly one port allocation mode.
- Define recursive merge behavior precisely: maps merge, while scalars, nulls, and arrays replace. Decide whether null deletes a value or is rejected.
- Reserve `webport dev` operation names (`check`, `config`, `status`, `logs`, `env`, `exec`, `stop`, and `clean`) so service selection is unambiguous. Document how a service with a reserved name is handled.
- Decide the v1 syntax for items mentioned but not fully specified by the request: dotenv files, local endpoints, profile environment, split stdout/stderr logs, bounded rotation, shell commands, and explicit identity overrides.
- Define the visibility and precedence of `route.export` aliases: which services receive them, whether they participate in `${env.NAME}` interpolation, and how name collisions are rejected.
- Resolve the same-project/same-branch worktree case. The draft requires same-branch worktrees to be locally isolated but the current route identity would collide; specify whether public routing requires an explicit identity override, gains a documented worktree discriminator, or fails preflight while loopback-only services continue.
- Resolve scope inconsistencies in the draft:
  - `discover: true` ports belong in v1 even though phase 1 only includes fixed/first-free allocation; schedule them for phase 2.
  - `restart: on-failure`, alternate failure policies, platform constraints, and generated container examples are phase 3 only if explicitly accepted into v1; otherwise defer them.
  - continuous health checks, detached sessions, `init`, includes, and editor schemas remain out of scope.
- Define stable human, JSON, and env output schemas and versioning expectations. Distinguish daemon commands such as `webport status`/`webport config` from session commands such as `webport dev status`/`webport dev config`.
- Distinguish ephemeral terminal diagnostics from retained summaries: a live failure message may include the failed PID, while the retained last-session record must not keep reusable PIDs.
- Add the finalized schema/CLI contract to project documentation or an architecture decision document and use it as the source for fixtures in later tasks.

Done when:

- No field used by the Cortex acceptance configuration is provisional.
- Every default, invalid combination, redaction rule, and CLI parse ambiguity has a documented answer.
- Deferred items are visibly marked and cannot accidentally expand the v1 implementation.

### 2. Establish test seams and shared primitives

Refactor only enough existing code to let the configured-session work reuse route, Git, API, clock, filesystem, process, and network behavior without invoking real external services.

Work:

- Extract command dispatch from `cmd/webport/cli.go` into testable functions that accept input/output streams and return errors instead of exiting.
- Expose canonical Git worktree-root detection from `cmd/webportctl/git` rather than duplicating `git rev-parse` calls. Cover normal repositories, linked worktrees, nested directories, detached HEAD, and non-Git overrides.
- Provide a reusable daemon client abstraction for readiness, `/config`, lease registration, heartbeat, recovery after daemon restart, and release. Preserve compatibility with legacy route endpoints where the existing route manager requires it.
- Provide injectable interfaces for clock/timers, random bytes, port probing, filesystem/runtime-directory discovery, command execution, and signals.
- Decide whether existing `cmd/webportctl/runtime` becomes a shared internal package or gains a stable API used by the new supervisor. Avoid copying its lease loop for multi-route sessions.
- Add test helpers for fake daemon responses, helper subprocesses, deterministic clocks/randomness, temporary worktrees, and failure injection.

Done when:

- Later unit tests can exercise planning and supervision without live Traefik, systemd, Docker, DNS, or a real Webport daemon.
- Existing route, daemon, and zero-configuration CLI tests continue to pass.

### 3. Implement project-file discovery and strict YAML loading

Build the raw configuration layer without starting commands or allocating resources.

Work:

- Add a YAML dependency that supports strict decoding and source-positioned errors.
- Starting at the current directory, search upward through the canonical Git/worktree root for the nearest `.webport.yaml` according to the task 1 contract.
- Make `--config PATH` select an explicit primary file and resolve the optional `.webport.local.yaml` beside it.
- Load the primary and local files separately, recursively merge their document trees, then strictly decode and validate the merged document as `version: 1`.
- Reject duplicate keys, unknown keys, unsupported versions, wrong scalar types, malformed durations, empty names, and invalid enum values with file/field context.
- Do not mutate the process environment or expand interpolation during raw loading.

Tests:

- Search from repository root and nested/monorepo directories, stop at the worktree boundary, explicit config selection, missing config, and unreadable files.
- Recursive map merge plus array/scalar replacement.
- Unknown/duplicate keys in either file, unsupported version, and error locations.
- Local override behavior without requiring the local file to exist.

Done when:

- `Load` returns a typed but unresolved configuration and deterministic diagnostics.
- Loading has no runtime side effects.

### 4. Implement identity, worktree scope, and path resolution

Resolve stable session identity before environment, commands, or routes are expanded.

Work:

- Infer project, branch, repository root, and canonical worktree root through the shared Git detection code; honor the explicit non-Git/monorepo overrides from task 1.
- Resolve all configured relative paths against the primary configuration directory, not the caller's incidental current directory. Apply this consistently to working directories, dotenv files, logs, exports, and project-secret storage where configurable.
- Derive a stable, filesystem- and Compose-safe `${session.scope}` from project plus canonical worktree path using a readable prefix and collision-resistant digest.
- Define a separate opaque session ID for one launch.
- Ensure two canonical paths cannot collide merely because their directory basenames match.

Tests:

- Normal checkout, linked worktrees on the same and different branches, symlinked invocation paths, non-Git override, and two same-named directories.
- Stable scope across launches in one worktree and different scope across worktrees.
- Relative-path resolution when invoked from a nested directory.

Done when:

- Identity and every base path needed by subsequent planning are immutable values in the resolved plan.

### 5. Add daemon configuration and route pre-resolution

Compute route identities, hostnames, and URLs before any child starts.

Work:

- Query the daemon's stable `/config` response and model base domain, default TTL, TLS mode, and optional CA path without ad hoc text parsing.
- Use `internal/route.BuildDomainChecked` for every hostname. Do not reproduce branch slug logic in the session packages.
- Resolve each route's project, inherited/overridden branch, raw route ID, hostname, HTTPS URL, target port reference, exports, and optional/required policy.
- Default non-primary route projects to `{session-project}-{service}` as specified by the contract.
- Detect duplicate raw identities, duplicate generated hostnames, invalid hostnames, and conflicting export names before startup.
- Required routes fail preflight when the daemon/config query is unavailable. Optional routes retain local service startup, leave lease state explicitly unavailable, and only expose public variables if their contract permits a resolved daemon configuration.
- Define stable JSON and env renderings for daemon configuration and route resolution.

Tests:

- Primary and non-primary route defaults, branch names containing slashes, explicit identities, hostname collisions, unavailable daemon for required/optional routes, and local-CA metadata.

Done when:

- A planner can provide exact public host/URL values to Vite-like children before route registration.

### 6. Upgrade the zero-configuration wrapper without changing its lifecycle

Deliver the low-risk compatibility improvement independently of configured sessions.

Work:

- Preserve `webport dev [existing route flags] -- COMMAND [ARG...]` parsing, stdin/stdout/stderr passthrough, listener discovery, lease behavior, signal forwarding, exit status, and process-group cleanup.
- Before child launch, resolve and inject:
  - `WEBPORT_PROJECT`;
  - `WEBPORT_BRANCH`;
  - `WEBPORT_ROUTE`;
  - `WEBPORT_HOST`;
  - `WEBPORT_URL`.
- Continue injecting the private discovery token and explicit app port as required internally, but never show the token in output or persisted metadata.
- Add the finalized `--format json` and `--format env` resolution interface at the CLI location chosen in task 1.
- Add regression tests proving arguments after `--` remain byte-for-byte equivalent and child exit/signal behavior has not changed.

Done when:

- Projects without `.webport.yaml` observe no wrapper behavior change except the documented generic environment variables and new opt-in inspection formats.

### 7. Implement environment composition and safe interpolation

Create the environment engine used by planning, children, route exports, `env`, and `exec`.

Work:

- Apply the exact precedence order: inherited environment, configured dotenv files, top-level env, profile env, then service env.
- Parse the supported dotenv syntax explicitly; do not source files through a shell.
- Implement only the allowed references: `${project}`, `${branch}`, `${session.scope}`, `${ports.NAME}`, `${env.NAME}`, `${routes.NAME.host}`, and `${routes.NAME.url}`.
- Resolve references as a dependency graph so forward references work while cycles and unknown names produce field-specific errors.
- Keep argument-array commands as discrete arguments. Reject sensitive-value interpolation into command arguments or shell text; require sensitive values to reach children through their environment.
- Track sensitivity as metadata through aliases and interpolation. A value derived from a sensitive value must also be sensitive.
- Define escaping/rendering rules independently for Bash/POSIX, Fish, JSON, and human-readable redacted output.

Tests:

- Every precedence level, forward references, cycles, missing references, literal special characters, no shell expansion, sensitive taint propagation, and forbidden secret-to-argv interpolation.

Done when:

- The same resolved environment data structure powers children and all output formats without reconstructing or reparsing values.

### 8. Implement generated values and project-lifetime secret storage

Add secure value generation after interpolation semantics and before live state exports.

Work:

- Generate values with `crypto/rand`, positive lengths, and either named character sets (`lower`, `upper`, `digit`, `hex`, `base64url`) or one explicit allowed-character string, never both.
- Generate session-lifetime values once per launch and retain them in supervisor memory unless an export is configured.
- Store project-lifetime values in a separate worktree-scoped, user-only secret file; atomically create/update it with mode `0600` and reject unsafe ownership/permissions according to the platform contract.
- Store enough non-secret metadata to validate that an existing value still satisfies its declared generation policy without printing the value.
- Implement `webport dev clean --secrets` as an explicit, narrowly scoped deletion operation. Normal shutdown must never remove project-lifetime secrets.
- Redact values by default from config, status, summaries, errors, and diagnostics. Permit terminal/env output only with explicit `--show-sensitive` where the contract allows it.

Tests:

- Character policies, invalid generators, deterministic fake randomness, reuse across launches, isolation across worktrees, file modes, atomic replacement, clean behavior, and redaction/taint propagation.

Done when:

- No Webport-controlled output or persisted non-secret state leaks a generated secret.

### 9. Implement named port allocation and collision checks

Resolve a unique port set before commands are started.

Work:

- Support fixed, first-free-at-or-above, and inclusive-range random allocation, plus phase-2 `discover: true` ownership for a service that selects its own listener.
- Validate bounds and ensure every resolved non-discovered port is unique in the session.
- Probe host availability on loopback before launch. Define IPv4/IPv6 behavior consistently with the backend host passed to routes.
- Associate each allocated port with its owning service so startup can perform a bounded reallocation/replan when a port race is detected before that owner becomes ready.
- Do not promise race-free reservation if Webport must release a socket for the child; make retry count and diagnostic behavior explicit.
- Reject references to a discovered port in values that must exist before the owning process reveals it, especially precomputed routes or earlier dependencies.

Tests:

- Fixed conflict, first-free scan, deterministic random selection, exhausted range, duplicate allocations, race/retry exhaustion, and illegal early use of discovered ports.

Done when:

- The resolved plan has a deterministic named-port map for the current launch and actionable allocation errors.

### 10. Build and validate the complete execution plan

Combine raw configuration, identity, routes, environment, ports, profiles, and preflight into one immutable plan before side effects.

Work:

- Validate service/profile names and references, then build the dependency DAG.
- Reject cycles with the complete cycle path, missing dependencies, empty profiles, conflicting routes, invalid working directories, and invalid completion/readiness/shutdown combinations.
- Expand the selected profile or service to its transitive dependency closure; ensure each service appears once.
- Produce stable topological ordering with deterministic tie-breaking for summaries and reverse ordering for cleanup.
- Check all `requires.commands` with `exec.LookPath` and report every missing executable in one preflight result.
- Resolve each command, working directory, environment, readiness check, endpoints, logs, route, completion behavior, and shutdown behavior without running anything.
- Render a redacted human plan and stable JSON plan for `webport dev check` and `webport dev config`; `check` performs validation/preflight while `config` displays the resolved configuration according to the contract.

Tests:

- DAG cycles and dependency closure, profiles, selected services, deterministic ordering, aggregate missing-command errors, invalid combinations, redacted output, and no-side-effect check/config commands.

Done when:

- The supervisor consumes an immutable plan and never has to reinterpret raw YAML during execution.

### 11. Implement the generic command runner and output capture

Generalize the existing single-child process-group code for multiple native or tool-managed commands.

Work:

- Start argument arrays directly without a shell; run explicit shell forms only through the configured platform shell contract.
- Set working directory and resolved environment without mutating Webport's own process environment.
- Create and supervise a separate process group for each long-running command on Linux and macOS.
- Capture stdout and stderr as line streams while retaining raw/plain content for files. Handle long lines and partial final lines without scanner-size surprises.
- Track start/end time, PID, exit code or signal, and whether exit occurred during intentional cleanup.
- Implement completion semantics:
  - `process` must remain alive and any unexpected exit, including status zero, is a required-service failure;
  - `exit` must finish successfully before readiness/dependents proceed.
- Generalize signal forwarding and bounded force-kill behavior without regressing descendant cleanup.

Tests:

- Argument preservation, environment/working directory, stdout/stderr capture, successful/failed one-shot commands, zero/nonzero long-running exits, signals, stubborn descendants, and leader-exits-before-descendant cases.

Done when:

- The runner reports structured lifecycle events and never independently decides session policy.

### 12. Implement readiness checks and local endpoints

Make startup readiness independently testable and cancellable.

Work:

- Implement TCP checks with per-probe and overall timeout.
- Implement HTTP(S) checks with configured method, expected status/status set, headers, interval, TLS policy allowed by the schema, per-probe timeout, and overall timeout.
- Implement argument-array command checks where exit status zero means ready, using the service working directory/environment without exposing sensitive values in argv.
- Cancel readiness immediately when its service exits, a dependency/session fails, or shutdown begins.
- Record the last probe result and a redacted error suitable for status and the final summary.
- Resolve and report named local endpoints even for services without public routes.

Tests:

- Success after retries, every check type, unexpected status, connection/probe timeout, overall timeout, cancellation, service exit during readiness, and redacted command-check errors.

Done when:

- A service cannot become `ready` until its start completion requirement and configured readiness check have both succeeded.

### 13. Deliver the phase-1 single-configured-process slice

Connect tasks 3–12 through `webport dev` for the smallest configured session before adding DAG concurrency.

Work:

- With a discovered config and no operation/service argument, select the default profile and require it to resolve to the supported phase-1 single process.
- Print the fully resolved redacted plan, then launch the command with environment and working directory.
- Support fixed/first-free port, one readiness check, one optional/required route, route exports, one log destination if present, and process-group cleanup.
- Activate the route only after readiness, renew it while the process runs, recover it after daemon restart, and release it after the process stops accepting traffic.
- Print local/public URL summary and actionable startup failure with the service/check name and captured output tail.
- Preserve the wrapper path whenever `-- COMMAND` is present, regardless of configuration-file presence.

Tests:

- End-to-end helper process with fake daemon, optional daemon absence, required route failure, route recovery, readiness timeout, Ctrl+C, child failure, and config-present wrapper compatibility.

Done when:

- The complete phase 1 behavior in the feature request works end to end and all earlier package tests remain green.

### 14. Implement dependency-aware concurrent startup

Expand the single-service runner into the phase-2 DAG scheduler.

Work:

- Start a service only after all dependencies are successful/ready.
- Start independent ready-to-run branches concurrently while emitting deterministic state transitions.
- Run each exit-completing service at most once per session, even when several dependents share it.
- Prevent dependents from starting after any required dependency fails.
- Support the default profile, named profile, and selected-service dependency closure.
- Keep startup cancellable and avoid goroutine/process leaks when one concurrent branch fails.

Tests:

- Diamond DAG, independent parallel branches, shared one-shot dependency, deterministic state, dependency failure, cancellation during concurrent startup, profiles, and selected-service launch.

Done when:

- Cortex-like infrastructure, setup tasks, backend, and frontend can be scheduled with the required ordering and safe concurrency.

### 15. Implement supervisor ownership, rollback, and shutdown policy

Centralize session lifetime rules in one foreground supervisor.

Work:

- Track running process groups, active route managers, and registered shutdown commands as owned responsibilities.
- Register an exit-completing service's shutdown command only after its start command succeeds, and register it unconditionally at that point even if later readiness fails.
- Keep the supervisor alive while it owns at least one process, route lease, or shutdown command. Let a plan containing only completed one-shots with no remaining responsibilities exit.
- On startup failure, required process exit, signal, or stop request:
  1. stop admitting new work and cancel readiness;
  2. signal running services so they stop accepting traffic;
  3. release route leases;
  4. terminate remaining process groups with their configured signal/grace period;
  5. run registered shutdown commands in reverse dependency order with individual Webport timeouts;
  6. remove live state/exports and retain the redacted final record.
- Preserve the initiating failure as primary; join/report cleanup failures without replacing it.
- Treat exits after intentional shutdown begins as cleanup events, not new initiating failures.
- Return nonzero for service failure or failed/timed-out cleanup, with the documented signal-to-exit behavior.

Tests:

- Normal Ctrl+C, SIGTERM/SIGHUP policy, one-shot readiness failure after shutdown registration, required process status-zero exit, reverse dependency cleanup, cleanup timeout/nonzero exit, multiple simultaneous failures, and original-error preservation.

Done when:

- Every supervisor exit path runs the same idempotent bounded cleanup state machine.

### 16. Generalize route management to multiple services

Integrate independent leases into the supervisor rather than wrapping services in nested `webport route` processes.

Work:

- Create one renewable lease manager per configured route with distinct client/lease IDs.
- Register a service route only after that service is ready; do not block unrelated un-routed services on optional routing failure.
- Track per-route pending, active, unavailable, recovering, failed, and released state.
- Re-register every active route after daemon restart/lease loss and update state without changing the precomputed hostname.
- Release routes after their services stop accepting traffic and before external-resource shutdown commands finish.
- Ensure route conflict errors name both service and raw identity, without exposing lease IDs in retained summaries.

Tests:

- Multiple routes, mixed optional/required routes, partial registration failure and rollback, daemon restart recovery, independent heartbeat failures, and cleanup ordering.

Done when:

- Frontend and Mailpit can maintain independent routes for the full foreground session.

### 17. Add worktree locking, runtime state, and the control endpoint

Make an active foreground session safely discoverable from other terminals.

Work:

- Select the per-user runtime/state directories on Linux and macOS and create them with user-only permissions.
- Key live state and lock ownership by a digest of the canonical worktree root while retaining the readable worktree path in metadata.
- Acquire a non-blocking one-session-per-worktree lock before allocating ports or starting commands.
- If locked, validate whether the recorded supervisor is live and report the active session plus state path; recover stale state safely without signaling unrelated reused PIDs.
- Implement the local supervisor control transport chosen in task 1, including authentication/permissions, request timeouts, schema versioning, and atomic metadata publication.
- Store only live operational data: session ID, control location, worktree/profile, selected ports, service state, PIDs, readiness, endpoints, route state, start time, log/export paths, and last redacted error.
- Never persist environment secrets, client discovery tokens, control tokens in world-readable metadata, or reusable process/lease IDs in the retained final record.

Tests:

- Lock contention, different worktrees, stale lock/state, PID reuse defense, permission checks, atomic reader behavior, malformed/unauthorized control requests, and cleanup after normal/failing shutdown.

Done when:

- A second `webport dev` reliably reports the active session rather than racing it, and other `webport dev` commands can reach the foreground owner.

### 18. Add environment exports and the retained last-session record

Persist only the configured integration artifacts with explicit security boundaries.

Work:

- Atomically render configured Bash/POSIX/Zsh, Fish, and JSON exports from the active resolved environment, using mode `0600` and correct shell-specific escaping.
- Write exports only after the plan is final and update them if a discovered runtime port becomes available.
- Remove live exports on clean/failing shutdown by default; define and implement any explicit retention override from task 1.
- Retain one atomically replaced, redacted last-session summary containing start/stop times, profile, final service states, initiating process/monitor and status/signal, cleanup errors, selected non-sensitive settings, endpoint/log paths, and route outcomes.
- Exclude sensitive values, live control credentials, route lease IDs, and reusable PIDs from the retained record.

Tests:

- Exact shell escaping, JSON schema, `0600` modes, atomic update, discovered-port refresh, cleanup, crash/stale behavior, summary replacement, and secret-scanning of every artifact.

Done when:

- Editors/tests can consume live environment data without stale files surviving an orderly session end.

### 19. Implement prefixed terminal output, log mirrors, and failure tails

Add observability without provider-specific log interpretation.

Work:

- Prefix/color interactive terminal lines by service while keeping redirected/plain output legible and file logs unprefixed.
- Support configured no-file, explicit file, or session-log-directory destinations; truncate/append; and combined/separate stdout/stderr according to the finalized schema.
- Create log directories safely and reject paths that violate the configuration-root/path policy.
- Maintain a bounded in-memory output tail per service for actionable startup/runtime errors.
- Ensure Webport never writes its own sensitive values into logs. Document that arbitrary child output cannot be reliably scrubbed and remains the child's responsibility.
- Record log paths in live state and the retained summary.

Tests:

- Concurrent partial lines, stdout/stderr separation, ANSI/color policy, append/truncate, file failures, bounded tails, redacted Webport diagnostics, and no interleaved/corrupted output under concurrency.

Done when:

- A failure names the service and check/process, shows a short useful tail, and points to the full log when one exists.

### 20. Implement session inspection commands

Expose the active resolved session consistently in human and machine-readable forms.

Work:

- `webport dev status`: query the foreground supervisor, or display the redacted retained record when no live session exists according to the contract.
- `webport dev config`: show the resolved configuration/plan without starting it; require `--show-sensitive` for values and keep sensitive command interpolation prohibited regardless.
- `webport dev env`: obtain the live environment from the supervisor and render Bash/POSIX/Zsh, Fish, or JSON; gate sensitive values behind `--show-sensitive`.
- Add `--format json` to status and other inspection commands defined in task 1, with stable field names for session ID, worktree, profile, services, PIDs, readiness, endpoints, routes, ports, start time, logs, and last error.
- Make human output explicitly say “development session” so it cannot be confused with daemon-level `webport status` and `webport config`.
- Define unavailable/no-session exit codes and diagnostics for automation.

Tests:

- Live/no-live/stale session, human/JSON/env output, all redaction gates, malformed supervisor response, and distinction from daemon status/config.

Done when:

- Another terminal can discover all non-sensitive operational facts without reading internal state files directly.

### 21. Implement operational control commands

Complete the phase-3 foreground-session operations.

Work:

- `webport dev logs [SERVICE] [--follow]`: resolve paths through live/retained metadata, validate service selection, stream existing content, and follow safely across the active file lifecycle.
- `webport dev exec SERVICE -- COMMAND`: ask the live supervisor for the service working directory and resolved environment, then run the one-off command with terminal passthrough and correct exit status. Do not implicitly enter containers.
- `webport dev stop`: authenticate to the foreground supervisor, request the same graceful shutdown state machine used by signals, wait/report the result with a bounded client timeout, and never signal a PID solely from stale metadata.
- `webport dev clean --secrets`: connect the explicit project-secret deletion from task 8, reject cleaning while a live session could be using the values unless the contract explicitly permits it.
- Implement bounded log rotation if it remains accepted in the v1 contract, and ensure `logs --follow` handles rotation.

Tests:

- Logs by service/all services, follow cancellation/rotation, missing logs, exec environment/directory/exit code, stop during startup and steady state, repeated stop, stale control metadata, and clean while active/inactive.

Done when:

- All phase-3 CLI operations use metadata/control APIs rather than guessing paths, parsing hostnames, or directly killing recorded PIDs.

### 22. Add end-to-end lifecycle and security tests

Exercise the complete behavior with helper processes and a fake daemon before relying on a real Cortex setup.

Work:

- Build a fixture configuration representing PostgreSQL/MinIO/Mailpit-style detached resources, one-shot setup tasks, backend/frontend processes, multiple routes, generated secrets, exports, and logs using local helper commands.
- Run two fixture worktrees concurrently and assert distinct scopes, locks, ports, routes, state, exports, and logs.
- Verify exact route host injection before frontend launch and route recovery after fake-daemon restart.
- Inject readiness failure, unexpected required-process exit, signal shutdown, cleanup failure, port races, and control-client stop.
- Scan terminal capture, Webport logs, JSON/status/config, runtime metadata, and retained summaries for known generated secret values and internal tokens.
- Confirm descendant processes are gone, leases are released, shutdown order is reversed, live state/exports are removed, final record remains, and persistent fixture data is untouched.
- Retain regression coverage for `webport dev -- COMMAND` with no config and with a config present.

Done when:

- Automated tests cover all ten Cortex replacement acceptance criteria without requiring Docker, Traefik, systemd, or LaunchDaemons.

### 23. Document, validate, and release the feature

Finish user-facing material and perform the repository-wide verification pass.

Work:

- Update `README.md`, CLI help, and `docs/architecture.md` with config discovery/merge, full schema, profiles, lifecycle semantics, environment precedence, routes, security, state paths, logs, cleanup, and command examples.
- Add a complete reviewed `.webport.yaml` example modeling Cortex's reusable orchestration while leaving migrations/bootstrap commands project-owned.
- Add ordinary Docker, Podman, attached Compose, detached Compose, wait-monitor, and log-follower examples. Clearly distinguish Webport timeout from runtime-tool timeout and avoid destructive cleanup examples by default.
- Document limitations: no detached mode, no runtime-specific state inference, no arbitrary runtime-port capture, no continuous health, no content-based child-log secret scrubbing, and no cleanup after SIGKILL/power loss.
- Update `verify.md` with a two-worktree manual scenario and foreground stop/failure checks.
- Run formatting, focused package tests during fixes, `go test ./...`/`mise run test`, `go vet ./...`/`mise run lint`, and cross-builds for Linux/macOS amd64/arm64. Run installer suites only if installation assets or behavior changed.
- Test a real Cortex conversion or equivalent representative project and record any contract corrections before declaring the feature complete.

Done when:

- Documentation and help match actual accepted syntax and output.
- The full automated suite, vet, and supported cross-build matrix pass.
- All acceptance criteria below have evidence from automated or recorded representative-project testing.

## Release acceptance checklist

- [ ] `webport dev` starts the full selected profile in dependency order and waits for configured readiness.
- [ ] Two worktrees run concurrently without sharing ports, route identities, locks, external-resource scope, runtime state, exports, or logs.
- [ ] Public hosts/URLs are available to children before launch, while leases activate only after readiness and recover after daemon restart.
- [ ] Ports, URLs, service state, logs, and permitted environment values are queryable from another terminal.
- [ ] Terminal output is service-prefixed and configured logs receive plain per-service output.
- [ ] Any required process exit ends the session, reports its status/signal and log tail, and returns nonzero.
- [ ] Ctrl+C and `webport dev stop` terminate process groups, release routes, run reverse-order bounded shutdown, and remove live state/exports.
- [ ] Persistent project data and project-lifetime secrets survive ordinary shutdown; only explicit configured commands or `clean --secrets` remove them.
- [ ] Known sensitive values and internal tokens are absent from default output, Webport logs, argv, live non-secret metadata, and retained summaries.
- [ ] `webport dev [route options] -- COMMAND` retains its existing lifecycle and passthrough behavior whether or not a project config exists.
- [ ] `webport status`/`webport config` still describe the daemon, while `webport dev status`/`webport dev config` clearly describe the project session.
- [ ] Linux and macOS builds and process-cleanup tests pass.

## Deferred follow-up tasks

These are deliberately outside the version 1 critical path unless task 1 explicitly promotes one:

- Add restart and alternate failure/keep-running policies after safe default supervision is proven.
- Add `webport init` and reviewed starter generation.
- Add reusable includes with repository-boundary safety.
- Publish an editor-friendly JSON Schema and completion integration.
- Add OS/architecture-conditioned services if not included in the final v1 schema.
- Add file-watch-triggered one-shot tasks and desktop notifications.
- Design a durable per-user supervisor protocol before implementing detached sessions.
- Evaluate path routing, middleware, headers, or arbitrary Traefik extensions as a separate API/security feature.
