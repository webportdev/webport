// A fake webport daemon for previewing the web UI without sessions. It
// implements the same /api contract as internal/webui/server.go with sample
// data that changes over time. Enabled by WEBPORT_WEBUI_MOCK=1; never
// shipped in the production bundle.
import type { IncomingMessage, ServerResponse } from "node:http";
import { randomBytes } from "node:crypto";
import type { Plugin } from "vite";
import type { Instance, Route, ServiceState } from "../src/types";

export const DEFAULT_MOCK_PASSWORD = "webport-test-pw";

const BASE = "webport.localhost";
const COOKIE = "webport_mock_session";

interface MockInstance extends Instance {
  env: Record<string, Record<string, string>>;
  sensitive: string[];
  restartingUntil?: number;
}

const started = Date.now();
const minutesAgo = (minutes: number) => new Date(Date.now() - minutes * 60_000).toISOString();

function service(state: string, ready: boolean, pid: number, last_error?: string): ServiceState {
  return { state, ready, pid: ready || state === "starting" ? pid : undefined, started_at: minutesAgo(42), last_error };
}

function seed(): MockInstance[] {
  return [
    {
      session_id: "mock-shop",
      name: "shop:feature/checkout",
      worktree: "/home/dev/src/shop/feature-checkout",
      profile: "default",
      started_at: minutesAgo(42),
      ports: { api: 4600, web: 4700, worker: 4800 },
      services: {
        api: service("ready", true, 41231),
        web: service("ready", true, 41262),
        worker: service("starting", false, 41290),
      },
      routes: {
        api: { state: "active", project: "shop", branch: "feature/checkout", url: `https://shop-feature-checkout.${BASE}`, optional: false },
        web: { state: "active", project: "shop-web", branch: "feature/checkout", url: `https://shop-web-feature-checkout.${BASE}`, optional: false },
      },
      endpoints: { "api.http": "http://127.0.0.1:4600", "web.http": "http://127.0.0.1:4700", "worker.metrics": "http://127.0.0.1:4800/metrics" },
      log_paths: {
        api: "/home/dev/src/shop/feature-checkout/.webport/logs/api.log",
        web: "/home/dev/src/shop/feature-checkout/.webport/logs/web.log",
        worker: "/home/dev/src/shop/feature-checkout/.webport/logs/worker.log",
      },
      env: {
        api: { PORT: "4600", LOG_LEVEL: "debug", DATABASE_URL: "postgres://shop:s3cr3t@127.0.0.1:5432/shop", SESSION_SECRET: "q8Zr2mKp1vXy7tLs9eWd4nHb", STRIPE_KEY: "sk_test_51MockMockMock" },
        web: { PORT: "4700", API_URL: `https://shop-feature-checkout.${BASE}`, NODE_ENV: "development" },
        worker: { QUEUE: "orders", CONCURRENCY: "4", DATABASE_URL: "postgres://shop:s3cr3t@127.0.0.1:5432/shop" },
      },
      sensitive: ["DATABASE_URL", "SESSION_SECRET", "STRIPE_KEY"],
    },
    {
      session_id: "mock-blog",
      name: "blog:main",
      worktree: "/home/dev/src/blog",
      profile: "default",
      started_at: minutesAgo(180),
      ports: { command: 3000 },
      services: { command: service("ready", true, 38211) },
      routes: { command: { state: "active", project: "blog", branch: "main", url: `https://blog-main.${BASE}`, optional: false } },
      endpoints: {},
      log_paths: { command: "/home/dev/src/blog/.webport/logs/command.log" },
      env: { command: { WEBPORT_PROJECT: "blog", WEBPORT_BRANCH: "main", WEBPORT_URL: `https://blog-main.${BASE}`, WEBPORT_APP_PORT: "3000" } },
      sensitive: [],
    },
    {
      session_id: "mock-analytics",
      name: "analytics:fix/charts",
      worktree: "/home/dev/src/analytics/fix-charts",
      profile: "full",
      started_at: minutesAgo(7),
      ports: { app: 5000 },
      services: { app: service("failed", false, 0, "exit status 1: Error: Cannot find module './charts/legend'") },
      routes: { app: { state: "pending", project: "analytics", branch: "fix/charts", optional: false } },
      endpoints: { "app.http": "http://127.0.0.1:5000" },
      log_paths: { app: "/home/dev/src/analytics/fix-charts/.webport/logs/app.log" },
      env: { app: { PORT: "5000", CHART_THEME: "dark" } },
      sensitive: [],
      last_error: "service app failed: exit status 1",
    },
  ];
}

let instances = seed();
const stoppedAt = new Map<string, number>();
const sessions = new Set<string>();

function publicInstance({ env: _env, sensitive: _sensitive, restartingUntil: _restart, ...rest }: MockInstance): Instance {
  return { ...rest, url: Object.values(rest.routes ?? {}).find((r) => r.url)?.url };
}

function tick() {
  const now = Date.now();
  // Stopped instances come back so the preview never runs out of data.
  for (const [id, at] of stoppedAt) {
    if (now - at > 15_000) {
      const original = seed().find((item) => item.session_id === id);
      if (original) instances.push(original);
      stoppedAt.delete(id);
    }
  }
  for (const instance of instances) {
    if (instance.restartingUntil && now >= instance.restartingUntil) {
      instance.restartingUntil = undefined;
      for (const [name, value] of Object.entries(instance.services)) {
        if (!value.last_error) instance.services[name] = { ...value, state: "ready", ready: true, started_at: new Date().toISOString() };
      }
    }
  }
  // The worker flips between starting and ready to show live updates.
  const worker = instances.find((item) => item.session_id === "mock-shop")?.services.worker;
  if (worker && !instances.find((item) => item.session_id === "mock-shop")?.restartingUntil) {
    const ready = Math.floor((now - started) / 8000) % 2 === 1;
    Object.assign(worker, { state: ready ? "ready" : "starting", ready });
  }
  instances.sort((a, b) => a.worktree.localeCompare(b.worktree));
}

function routes(webuiPort: number): Route[] {
  const result: Route[] = [];
  for (const instance of instances) {
    for (const [name, route] of Object.entries(instance.routes ?? {})) {
      if (!route.url) continue;
      result.push({
        project: route.project, branch: route.branch, port: instance.ports?.[name] ?? 0,
        domain: new URL(route.url).host, created_at: instance.started_at,
        expires_at: new Date(Date.now() + 25_000).toISOString(), source: "manual",
      });
    }
  }
  result.push({ project: "legacy", branch: "main", port: 8081, domain: `legacy-main.${BASE}`, created_at: minutesAgo(3), expires_at: new Date(Date.now() + 12_000).toISOString(), source: "manual" });
  result.push({ project: "webport", branch: "webui", port: webuiPort, domain: `webport.${BASE}`, created_at: new Date(started).toISOString(), expires_at: "0001-01-01T00:00:00Z", source: "system" });
  return result.sort((a, b) => a.domain.localeCompare(b.domain));
}

function logLines(instanceID: string, serviceName: string): string[] {
  const now = Date.now();
  const lines: string[] = [];
  const count = Math.min(200, 40 + Math.floor((now - started) / 1000));
  for (let i = count - 1; i >= 0; i--) {
    const time = new Date(now - i * 1000).toTimeString().slice(0, 8);
    if (instanceID === "mock-analytics") {
      lines.push(i % 7 === 0 ? `${time} Error: Cannot find module './charts/legend'` : `${time} [vite] page reload src/charts/index.ts`);
    } else if (serviceName === "worker") {
      lines.push(`${time} worker processed order #${10_000 + count - i} in ${20 + ((count - i) % 9) * 7}ms`);
    } else {
      const status = (count - i) % 13 === 0 ? 500 : (count - i) % 5 === 0 ? 304 : 200;
      lines.push(`${time} ${serviceName} GET /api/cart/${(count - i) % 40} ${status} ${3 + ((count - i) % 11)}ms`);
    }
  }
  return lines;
}

function send(res: ServerResponse, status: number, body: unknown) {
  res.statusCode = status;
  res.setHeader("Content-Type", "application/json");
  res.setHeader("Cache-Control", "no-store");
  res.end(JSON.stringify(body));
}

const error = (res: ServerResponse, status: number, message: string) => send(res, status, { error: message });

function readBody(req: IncomingMessage): Promise<string> {
  return new Promise((resolve) => {
    let data = "";
    req.on("data", (chunk) => (data = (data + chunk).slice(0, 4096)));
    req.on("end", () => resolve(data));
  });
}

function signedIn(req: IncomingMessage): boolean {
  const cookie = req.headers.cookie ?? "";
  const token = /(?:^|;\s*)webport_mock_session=([^;]+)/.exec(cookie)?.[1];
  return Boolean(token && sessions.has(token));
}

export function mockBackend(password: string): Plugin {
  return {
    name: "webport-mock-backend",
    configureServer(server) {
      server.middlewares.use(async (req, res, next) => {
        const url = new URL(req.url ?? "/", "http://mock");
        if (!url.pathname.startsWith("/api/")) return next();
        // Mirror the real server: writes need the same-origin request header.
        if (req.method !== "GET" && req.headers["x-webport-request"] !== "1") return error(res, 403, "cross-site request rejected");
        tick();
        const path = url.pathname;

        if (path === "/api/auth" && req.method === "GET") return send(res, 200, { authenticated: signedIn(req), password_set: true });
        if (path === "/api/login" && req.method === "POST") {
          let attempt = "";
          try {
            attempt = (JSON.parse(await readBody(req)) as { password?: string }).password ?? "";
          } catch {
            return error(res, 400, "invalid request body");
          }
          if (attempt !== password) return error(res, 401, "Incorrect password.");
          const token = randomBytes(24).toString("base64url");
          sessions.add(token);
          res.setHeader("Set-Cookie", `${COOKIE}=${token}; Path=/; HttpOnly; SameSite=Strict`);
          return send(res, 200, { authenticated: true });
        }
        if (path === "/api/logout" && req.method === "POST") {
          res.setHeader("Set-Cookie", `${COOKIE}=; Path=/; Max-Age=0`);
          return send(res, 200, { authenticated: false });
        }
        if (!signedIn(req)) return error(res, 401, "sign in required");

        if (path === "/api/snapshot" && req.method === "GET") {
          const port = server.config.server.port ?? 5173;
          const routeList = routes(port);
          return send(res, 200, {
            instances: instances.map(publicInstance),
            routes: routeList,
            daemon: {
              version: "v0.14.0-mock", uptime_seconds: Math.floor((Date.now() - started) / 1000) + 86_400 * 2 + 3_600 * 5,
              tls_mode: "local-ca", route_count: routeList.length, lease_count: routeList.length - 1,
              revision: 128 + Math.floor((Date.now() - started) / 10_000), applied_revision: 128 + Math.floor((Date.now() - started) / 10_000),
              last_published: new Date(Date.now() - 4_000).toISOString(), ready: true,
              webport_api: "http://127.0.0.1:8080", session_socket: "/run/webport/webport.sock",
              connected_sessions: instances.length, traefik_https_listener: "listening at 127.0.0.1:443",
            },
            webui: { url: `https://webport.${BASE}`, subdomain: "webport", listen: `127.0.0.1:${port}` },
            generated_at: new Date().toISOString(),
          });
        }

        const match = /^\/api\/instances\/([^/]+)\/([a-z]+)$/.exec(path);
        const instance = match ? instances.find((item) => item.session_id === decodeURIComponent(match[1]!)) : undefined;
        if (match && !instance) return error(res, 404, "Instance is no longer active.");
        if (instance && match) {
          const operation = match[2];
          const flag = (name: string) => url.searchParams.get(name) === "1";
          const environment = (sensitive: boolean, inherited: boolean) => {
            const result: Record<string, Record<string, string>> = {};
            for (const [name, values] of Object.entries(instance.env)) {
              result[name] = {};
              for (const [key, value] of Object.entries(values)) {
                result[name]![key] = !sensitive && instance.sensitive.includes(key) ? "<redacted>" : value;
              }
              if (inherited) Object.assign(result[name]!, { HOME: "/home/dev", PATH: "/usr/local/bin:/usr/bin:/bin", SHELL: "/bin/zsh", TERM: "xterm-256color", AWS_SECRET_ACCESS_KEY: sensitive ? "wJalrXUtnFEMI/K7MDENG" : "<redacted>" });
            }
            return result;
          };
          if (operation === "inspect" && req.method === "GET") {
            return send(res, 200, { project: instance.name.split(":")[0], branch: instance.name.split(":")[1], worktree: instance.worktree, profile: instance.profile, routes: [], environment: environment(flag("sensitive"), flag("inherited")) });
          }
          if (operation === "env" && req.method === "GET") {
            const value = environment(true, flag("inherited"))[url.searchParams.get("service") ?? ""]?.[url.searchParams.get("name") ?? ""];
            return value === undefined ? error(res, 404, "Environment entry is no longer available.") : send(res, 200, { value });
          }
          if (operation === "logs" && req.method === "GET") {
            const name = url.searchParams.get("service") ?? "";
            if (!instance.services[name]) return error(res, 404, `Service "${name}" is not part of this instance.`);
            return send(res, 200, { lines: logLines(instance.session_id, name) });
          }
          if ((operation === "stop" || operation === "restart") && req.method === "POST") {
            if (operation === "stop") {
              instances = instances.filter((item) => item !== instance);
              stoppedAt.set(instance.session_id, Date.now());
            } else {
              instance.restartingUntil = Date.now() + 3_000;
              for (const [name, value] of Object.entries(instance.services)) {
                if (!value.last_error) instance.services[name] = { ...value, state: "starting", ready: false };
              }
            }
            return send(res, 202, { message: `${operation} requested; waiting for session state` });
          }
        }
        return error(res, 404, "not found");
      });
    },
  };
}
