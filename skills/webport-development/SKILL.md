---
name: webport-development
description: Use Webport to run, inspect, troubleshoot, and expose local development servers through stable HTTPS routes.
---

# Webport Development

Use Webport for local development servers and configured development sessions.

## Start a development server

- In a checkout without a configured session, run `webport dev -- COMMAND`.
- Use `webport dev --port PORT -- COMMAND` when the application exposes more
  than one listener or does not use the inferred port.
- When the project has `.webport.yaml`, use `webport dev` for its configured
  session. Use `webport dev check` before starting it when configuration may be
  invalid.
- Sessions run in the foreground by default. Use `webport dev -d` (or
  `--detach`) for a configured background session, or
  `webport dev -d -- COMMAND` for a detached command wrapper. Put `-d` before
  `--` so it is consumed by Webport rather than the application.
- Detached startup waits for readiness and prints the supervisor PID and log
  path. Check that log if startup fails; use `--startup-timeout DURATION` to
  adjust the wait. Detached mode cannot be combined with inspection commands
  or `--format`.
- Treat the printed `WEBPORT_URL` or route URL as the public development URL.

## Inspect and troubleshoot

- Run `webport inspect` from the current project to see active HTTPS URLs and
  environment values. For another active configured worktree, use
  `webport inspect --config /path/to/.webport.yaml`.
- A configured session's inspection groups resolved environment values by
  service, including sensitive values. Use `--format json` for structured
  output and `--include-inherited` when the process environment matters.
- For a wrapper route without `.webport.yaml`, inspection shows the active URL
  and route context derived from it. It cannot read the child's full
  environment. Use `webport list` to find all active routes, including routes
  with identities overridden from Git defaults.
- Use `webport dev status` for live or retained session state,
  `webport dev logs SERVICE` for logs, and `webport dev env` for shell-ready
  environment output. Use `webport dev config` to preview an inactive session.
- Use `webport status`, `webport config`, and `webport doctor` for daemon and
  route problems.
- `webport upgrade` replaces the installed `webport`, `webportctl`, and
  `webport-dns` binaries and refreshes already-installed Webport skills for the
  invoking user without prompting, including locally edited skill files.
  Absent skill installations are left untouched; `--dry-run` reports planned
  updates without writing. Running `webport dev` sessions keep their existing
  supervisor process until stopped and launched again; `webport inspect` can
  read older sessions.
- For an active configured session, use `webport dev restart` to gracefully
  stop its services and leases, reload configuration, and start again with the
  active profile and service selection. The command acknowledges the request;
  use `webport dev status` and `webport dev logs SERVICE --follow` to verify
  readiness or diagnose restart failures. Use `--config PATH` for another
  configured project.
- Use `webport dev stop` to request a graceful configured session stop from
  another terminal. These control commands work for foreground and detached
  configured sessions; command wrappers do not expose session control. For a
  detached wrapper, send SIGTERM to the printed supervisor PID for cleanup.
- Restart reuses the running supervisor binary. After `webport upgrade`, stop
  and launch the session again to use the newly installed binary.
- If a route is missing, first check that the client or session is still
  running and renewing its lease; manual routes expire unless refreshed.

## Routes and safety

- Use `webport route --port PORT` for an already-running server and let Webport
  infer the Git project and branch when possible.
- Route identity is `project:branch`; it is distinct from the generated
  hostname slug. Preserve branch slashes in identities and never reconstruct an
  identity by parsing a hostname.
- Prefer the wrapper or configured session over legacy daemon-side process
  discovery. Use `webportctl` only when the process context cannot be wrapped.
- Do not put DNS provider credentials or other secrets in command-line
  arguments. Use the installer’s credentials-file workflow.

## Repository changes

When changing Webport itself, read the repository `AGENTS.md`, preserve
deterministic Traefik output and atomic publication, and run focused Go tests
before the full test suite. Changes to installer or platform assets also need
the relevant installer test.

When adding or changing features, update this skill and any affected supporting
resources in the same change so installed agent guidance matches the CLI.
