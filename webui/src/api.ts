import type { Inspection, LogsResponse, Operation, Snapshot } from "./types";

/** Raised for HTTP 401 so the app can return to the sign-in screen. */
export class UnauthorizedError extends Error {}

export class APIError extends Error {
  constructor(
    message: string,
    readonly status: number,
  ) {
    super(message);
  }
}

async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  // Required on writes; a cross-site page cannot send it without CORS approval.
  headers.set("X-Webport-Request", "1");
  if (init.body) headers.set("Content-Type", "application/json");
  const response = await fetch(path, { ...init, headers, credentials: "same-origin", cache: "no-store" });
  let body: unknown = null;
  try {
    body = await response.json();
  } catch {
    // Non-JSON responses fall through to the status message.
  }
  if (response.status === 401 && path !== "/api/login") throw new UnauthorizedError("Sign in required");
  if (!response.ok) {
    const message =
      body && typeof body === "object" && "error" in body && typeof body.error === "string"
        ? body.error
        : `Request failed (HTTP ${response.status})`;
    throw new APIError(message, response.status);
  }
  return body as T;
}

const instancePath = (id: string, suffix: string) => `/api/instances/${encodeURIComponent(id)}/${suffix}`;

export const api = {
  auth: () => request<{ authenticated: boolean; password_set: boolean }>("/api/auth"),
  login: (password: string) =>
    request<{ authenticated: boolean }>("/api/login", { method: "POST", body: JSON.stringify({ password }) }),
  logout: () => request<{ authenticated: boolean }>("/api/logout", { method: "POST" }),
  snapshot: (signal?: AbortSignal) => request<Snapshot>("/api/snapshot", { signal }),
  inspect: (id: string, sensitive: boolean, inherited: boolean, signal?: AbortSignal) =>
    request<Inspection>(
      instancePath(id, `inspect?sensitive=${sensitive ? 1 : 0}&inherited=${inherited ? 1 : 0}`),
      { signal },
    ),
  envValue: (id: string, service: string, name: string, inherited: boolean) =>
    request<{ value: string }>(
      instancePath(
        id,
        `env?${new URLSearchParams({ service, name, inherited: inherited ? "1" : "0" }).toString()}`,
      ),
    ),
  logs: (id: string, service: string, signal?: AbortSignal) =>
    request<LogsResponse>(instancePath(id, `logs?${new URLSearchParams({ service }).toString()}`), { signal }),
  control: (id: string, operation: Operation) =>
    request<{ message: string }>(instancePath(id, operation), { method: "POST" }),
};
