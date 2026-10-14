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
- `webport -d` is a shorthand for `webport dev -d`. Detached startup waits for
  readiness and prints the supervisor PID, log path, published service URLs,
  and configured endpoint URLs. Check that log if startup fails; use `--startup-timeout DURATION` to
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
- If a route is missing, first check that the client or session is still
  running and renewing its lease; manual routes expire unless refreshed.
- `webport upgrade` replaces the installed binaries and refreshes installed
  Webport skills; `--dry-run` previews it. If `webport --version` stays old,
  compare `command -v webport` with `/usr/local/bin/webport --version` and
  resolve the shadowing copy the upgrade reported.
- `webport tui` is an interactive dashboard for people. Do not launch it from
  an agent; it takes over the terminal and does not exit on its own.

## Stop and restart

- `webport dev stop` and `webport dev restart` request a graceful stop or
  restart of a foreground or detached configured session or command wrapper.
  Without arguments they target the current worktree's configured session,
  then its only command wrapper. Use `--config PATH` for another configured
  project, or a route ID such as `webport dev stop myapp:feature/auth` to
  target any instance from any directory.
- If the target is ambiguous, the error lists route IDs or supervisor PIDs;
  pass one route, or send SIGTERM to the listed PID.
- Both commands only acknowledge the request. Verify with `webport dev status`
  or `webport list`, and diagnose restart failures with
  `webport dev logs SERVICE --follow`.
- A configured restart reloads configuration and keeps the active profile and
  service selection. Restart reuses the running supervisor binary, so after
  `webport upgrade` stop and launch the session again.

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
