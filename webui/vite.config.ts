import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// During development, "mise run dev" serves the daemon's web UI on
// 127.0.0.1:7357 and "mise run dev-webui" proxies the API to it. The proxy
// keeps the browser's Host so same-origin checks pass.
const devAPI = process.env.WEBPORT_WEBUI_DEV_API ?? "http://127.0.0.1:7357";

export default defineConfig({
  plugins: [react()],
  // The Go binary embeds dist/, so this build must run before "go build".
  build: {
    outDir: "dist",
    emptyOutDir: true,
    sourcemap: false,
    assetsInlineLimit: 0,
  },
  server: {
    proxy: {
      "/api": { target: devAPI, changeOrigin: false },
    },
  },
  test: {
    environment: "node",
  },
} as Parameters<typeof defineConfig>[0]);
