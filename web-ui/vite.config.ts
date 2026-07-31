/// <reference types="vitest" />
import { defineConfig } from "vite";
import react from "@vitejs/plugin-react";

// Dev-server proxies. Both route families target the composed backend.
// Override to point `npm run dev` at a remote stack:
//   VITE_LINEARCAST_HOST=x.x.x.x npm run dev
const linearcastTarget = `http://${process.env.VITE_LINEARCAST_HOST || "127.0.0.1"}:${process.env.VITE_LINEARCAST_PORT || "8888"}`;

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
        target: linearcastTarget,
        changeOrigin: true,
        // The admin enforces a same-origin check on writes when a password
        // is set. From the dev server the browser sends Origin: localhost:5173,
        // which the admin rejects. Rewrite Origin to match the target so the
        // admin sees the proxied request as same-origin. The target is the
        // composed backend — admin and playback have shared one listener since
        // the separate admin origin went away.
        configure: (proxy) => {
          proxy.on("proxyReq", (proxyReq) => {
            proxyReq.setHeader("origin", linearcastTarget);
            proxyReq.removeHeader("referer");
          });
        },
      },
    },
  },
});
