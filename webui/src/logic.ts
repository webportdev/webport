import type { Instance, Route, ServiceState } from "./types";

export type Tone = "good" | "warn" | "bad";

/** Only link to plain HTTP(S) URLs without embedded credentials. */
export function safeURL(value: string | undefined): string | undefined {
  if (!value) return undefined;
  try {
    const parsed = new URL(value);
    if ((parsed.protocol === "https:" || parsed.protocol === "http:") && !parsed.username && !parsed.password) {
      return value;
    }
  } catch {
    // Not a URL.
  }
  return undefined;
}

export function serviceNames(instance: Instance | undefined): string[] {
  return instance ? Object.keys(instance.services).sort() : [];
}

export function readyCount(instance: Instance): { ready: number; total: number } {
  const services = Object.values(instance.services);
  return { ready: services.filter((s) => s.ready).length, total: services.length };
}

/**
 * Mirrors the terminal dashboard: a ready instance with an error, such as a
 * recovering route lease, is degraded rather than failed.
 */
export function instanceTone(instance: Instance): Tone {
  const { ready, total } = readyCount(instance);
  const failed = Boolean(instance.last_error) || Object.values(instance.services).some((s) => s.last_error);
  const allReady = ready === total && ready > 0;
  if (failed && !allReady) return "bad";
  if (allReady && !failed) return "good";
  return "warn";
}

export function serviceTone(service: ServiceState): Tone {
  if (service.last_error) return "bad";
  return service.ready ? "good" : "warn";
}

/** First published URL for service, or for any service when it is empty. */
export function instanceURL(instance: Instance, service = ""): string | undefined {
  const routes = instance.routes ?? {};
  for (const name of Object.keys(routes).sort()) {
    if (service && name !== service) continue;
    const url = safeURL(routes[name]?.url);
    if (url) return url;
  }
  const endpoints = instance.endpoints ?? {};
  for (const name of Object.keys(endpoints).sort()) {
    if (service && !name.startsWith(service + ".")) continue;
    const url = safeURL(endpoints[name]);
    if (url) return url;
  }
  return undefined;
}

export interface URLRow {
  id: string;
  name: string;
  kind: "public" | "endpoint";
  state?: string;
  url: string;
}

export function urlRows(instance: Instance | undefined, service: string): URLRow[] {
  if (!instance) return [];
  const rows: URLRow[] = [];
  for (const [name, route] of Object.entries(instance.routes ?? {})) {
    if (service && name !== service) continue;
    if (route.url) rows.push({ id: name + ".public", name, kind: "public", state: route.state, url: route.url });
  }
  for (const [name, url] of Object.entries(instance.endpoints ?? {})) {
    if (service && !name.startsWith(service + ".")) continue;
    rows.push({ id: name, name, kind: "endpoint", url });
  }
  return rows.sort((a, b) => (a.id < b.id ? -1 : a.id > b.id ? 1 : 0));
}

export const routeID = (route: Route) => `${route.project}:${route.branch}`;
export const backendHost = (host?: string) => host || "127.0.0.1";

export function matches(filter: string, ...values: (string | number | undefined)[]): boolean {
  if (!filter) return true;
  const needle = filter.toLowerCase();
  return values.some((value) => value !== undefined && String(value).toLowerCase().includes(needle));
}

export function formatValue(value: unknown): string {
  if (value === null || value === undefined) return "";
  if (typeof value === "string") return value;
  if (typeof value === "number" || typeof value === "boolean") return String(value);
  return JSON.stringify(value);
}

/** Human labels for well-known daemon status keys; others are shown as-is. */
export const daemonLabels: Record<string, string> = {
  version: "Version",
  uptime_seconds: "Uptime",
  tls_mode: "TLS mode",
  route_count: "Routes",
  lease_count: "Leases",
  revision: "Revision",
  applied_revision: "Applied revision",
  last_published: "Last published",
  last_error: "Last error",
  ready: "Ready",
  webport_api: "Webport API",
  traefik_https_listener: "Traefik HTTPS listener",
};

export function formatDaemonValue(key: string, value: unknown): string {
  if (key === "uptime_seconds" && typeof value === "number") return formatDuration(value);
  if (key === "last_published" && typeof value === "string" && value && !value.startsWith("0001-")) {
    return formatTime(value);
  }
  if (typeof value === "boolean") return value ? "Yes" : "No";
  return formatValue(value);
}

export function formatDuration(seconds: number): string {
  const units: [number, string][] = [
    [86400, "d"],
    [3600, "h"],
    [60, "m"],
  ];
  const parts: string[] = [];
  let remaining = Math.max(0, Math.floor(seconds));
  for (const [size, label] of units) {
    if (remaining >= size) {
      parts.push(`${Math.floor(remaining / size)}${label}`);
      remaining %= size;
    }
  }
  if (parts.length < 2) parts.push(`${remaining}s`);
  return parts.slice(0, 2).join(" ");
}

export function formatTime(value: string | undefined): string {
  if (!value || value.startsWith("0001-")) return "—";
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return value;
  return date.toLocaleString(undefined, { month: "short", day: "2-digit", hour: "2-digit", minute: "2-digit", second: "2-digit" });
}

export function relativeTime(value: string | undefined, now: number): string {
  if (!value || value.startsWith("0001-")) return "";
  const time = new Date(value).getTime();
  if (Number.isNaN(time)) return "";
  const delta = Math.round((time - now) / 1000);
  const abs = Math.abs(delta);
  const text = abs < 60 ? `${abs}s` : formatDuration(abs);
  return delta >= 0 ? `in ${text}` : `${text} ago`;
}
