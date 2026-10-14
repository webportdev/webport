import { describe, expect, it } from "vitest";
import { formatDuration, instanceTone, instanceURL, safeURL, urlRows } from "./logic";
import type { Instance } from "./types";

const base: Instance = {
  session_id: "s1",
  name: "app:main",
  worktree: "/src/app",
  profile: "default",
  started_at: "2026-01-01T00:00:00Z",
  services: {},
};

describe("safeURL", () => {
  it("accepts plain HTTP(S) URLs", () => {
    expect(safeURL("https://app-main.example.com")).toBe("https://app-main.example.com");
    expect(safeURL("http://127.0.0.1:3000/x")).toBe("http://127.0.0.1:3000/x");
  });
  it("rejects scripts and credentials", () => {
    expect(safeURL("javascript:alert(1)")).toBeUndefined();
    expect(safeURL("https://user:pw@example.com")).toBeUndefined();
    expect(safeURL("not a url")).toBeUndefined();
  });
});

describe("instanceTone", () => {
  it("matches the terminal dashboard readiness rules", () => {
    expect(instanceTone(base)).toBe("warn");
    const ready = { ...base, services: { web: { state: "running", ready: true } } };
    expect(instanceTone(ready)).toBe("good");
    expect(instanceTone({ ...ready, last_error: "lease recovering" })).toBe("warn");
    const failed = { ...base, services: { web: { state: "failed", ready: false, last_error: "exit 1" } } };
    expect(instanceTone(failed)).toBe("bad");
  });
});

describe("urls", () => {
  const instance: Instance = {
    ...base,
    routes: { web: { state: "active", project: "app", branch: "main", url: "https://app-main.example.com", optional: false } },
    endpoints: { "web.http": "http://127.0.0.1:3000", "api.http": "http://127.0.0.1:4000" },
  };
  it("prefers public routes", () => {
    expect(instanceURL(instance)).toBe("https://app-main.example.com");
    expect(instanceURL(instance, "api")).toBe("http://127.0.0.1:4000");
  });
  it("lists a service's public and local URLs", () => {
    expect(urlRows(instance, "web").map((row) => row.id)).toEqual(["web.http", "web.public"]);
  });
});

describe("formatDuration", () => {
  it("uses two significant units", () => {
    expect(formatDuration(5)).toBe("5s");
    expect(formatDuration(65)).toBe("1m 5s");
    expect(formatDuration(90061)).toBe("1d 1h");
  });
});
