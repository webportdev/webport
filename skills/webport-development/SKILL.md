---
name: webport-development
description: Use Webport to run, inspect, troubleshoot, and expose local development servers through stable HTTPS routes.
---

# Webport Development

Use Webport for local development servers and foreground development sessions.

## Start a development server

- In a checkout without a configured session, run `webport dev -- COMMAND`.
- Use `webport dev --port PORT -- COMMAND` when the application exposes more
  than one listener or does not use the inferred port.
- When the project has `.webport.yaml`, use `webport dev` for its configured
  session. Use `webport dev check` before starting it when configuration may be
  invalid.
- Treat the printed `WEBPORT_URL` or route URL as the public development URL.

## Inspect and troubleshoot

- Use `webport dev status`, `webport dev logs SERVICE`, and `webport dev env`
  for a configured session.
- Use `webport list`, `webport status`, `webport config`, and `webport doctor`
  for daemon and route problems.
- Use `webport dev stop` to request a graceful stop from another terminal.
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
