import { renderToStaticMarkup } from "react-dom/server";
import { describe, expect, it, vi } from "vitest";
import { serviceRows, type RowActions } from "./rows";
import type { Instance } from "./types";

const actions: RowActions = {
  openInstance: vi.fn(),
  openService: vi.fn(),
  control: vi.fn(),
  copyURL: vi.fn(async () => {}),
  copyEnv: vi.fn(async () => {}),
  expand: vi.fn(),
};

const instance: Instance = {
  session_id: "s1",
  name: "app:main",
  worktree: "/src/app",
  profile: "default",
  started_at: "2026-01-01T00:00:00Z",
  services: {},
};

describe("service descriptions", () => {
  it("shows descriptions as searchable plain text in rows, details, and expanded views", () => {
    const description = "Application frontend <script>alert('x')</script>";
    const row = serviceRows({ ...instance, services: { web: { state: "ready", ready: true, description } } }, actions)[0]!;

    expect(row.search).toContain(description);
    expect(row.expand.text).toContain(description);
    for (const content of [row.primary, row.detail]) {
      const markup = renderToStaticMarkup(content);
      expect(markup).toContain("Application frontend &lt;script&gt;");
      expect(markup).not.toContain("<script>");
    }
    expect(renderToStaticMarkup(row.detail)).toContain("Description");
  });

  it.each([undefined, ""])("omits the description field when unset (%s)", (description) => {
    const row = serviceRows({ ...instance, services: { web: { state: "ready", ready: true, description } } }, actions)[0]!;
    expect(renderToStaticMarkup(row.detail)).not.toContain("Description");
    expect(row.expand.text).toBe("web\nready");
  });
});
