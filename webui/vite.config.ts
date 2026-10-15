import { defineConfig, type ServerOptions } from "vite";
import react from "@vitejs/plugin-react";
import { DEFAULT_MOCK_PASSWORD, mockBackend } from "./mock/backend";

// "npm run dev:mock" (or "webport dev" from the repository root) serves the
// UI with an in-process fake backend for previews; nothing else is needed.
const mock = process.env.WEBPORT_WEBUI_MOCK === "1";
const mockPassword = process.env.WEBPORT_WEBUI_MOCK_PASSWORD || DEFAULT_MOCK_PASSWORD;

// Otherwise "mise run dev" serves the daemon's web UI on 127.0.0.1:7357 and
// the proxy forwards the API to it, keeping the browser's Host so
// same-origin checks pass.
const devAPI = process.env.WEBPORT_WEBUI_DEV_API ?? "http://127.0.0.1:7357";

// When webport publishes this dev server, accept its HTTPS hostname and send
// hot-reload traffic through Traefik's port rather than Vite's.
function publicServer(): ServerOptions {
  const host = process.env.WEBPORT_HOST;
  const url = process.env.WEBPORT_URL;
  if (!host) return {};
  return {
    allowedHosts: [host],
    hmr: url?.startsWith("https://") ? { clientPort: 443, protocol: "wss" } : undefined,
  };
}

export default defineConfig({
  plugins: [react(), ...(mock ? [mockBackend(mockPassword)] : [])],
  // The Go binary embeds dist/, so this build must run before "go build".
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    assetsInlineLimit: 0,
  },
  server: {
    ...publicServer(),
    proxy: mock ? undefined : { "/api": { target: devAPI, changeOrigin: false } },
  },
  test: {
    environment: "node",
  },
} as Parameters<typeof defineConfig>[0]);
