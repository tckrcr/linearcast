/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Dev-server proxies. Defaults target backend binaries running on this host.
// Override to point `npm run dev` at a remote stack:
//   VITE_LINEARCAST_HOST=x.x.x.x VITE_ADMIN_HOST=x.x.x.x npm run dev
const linearcastTarget = `http://${process.env.VITE_LINEARCAST_HOST || "127.0.0.1"}:${process.env.VITE_LINEARCAST_PORT || "8888"}`;
const adminTarget = `http://${process.env.VITE_ADMIN_HOST || "127.0.0.1"}:${process.env.VITE_ADMIN_PORT || "8890"}`;

export default defineConfig({
  plugins: [react()],
  test: {
    environment: "jsdom",
    globals: true,
  },
  build: {
    // hls.js is ~523 kB minified; raise the warning floor just above it so
    // we still catch anything else that balloons unexpectedly.
    chunkSizeWarningLimit: 550,
  },
  server: {
    port: 5173,
    proxy: {
      "/hls": {
        target: linearcastTarget,
        changeOrigin: true,
        rewrite: (path) => path.replace(/^\/hls/, ""),
      },
      "/channel": {
        target: linearcastTarget,
        changeOrigin: true,
      },
      "/api": {
        target: adminTarget,
        changeOrigin: true,
        // The admin enforces a same-origin check on writes when a password
        // is set. From the dev server the browser sends Origin: localhost:5173,
        // which the admin rejects. Rewrite Origin to match the target so the
        // admin sees the proxied request as same-origin.
        configure: (proxy) => {
          proxy.on("proxyReq", (proxyReq) => {
            proxyReq.setHeader("origin", adminTarget);
            proxyReq.removeHeader("referer");
          });
        },
      },
    },
  },
});
