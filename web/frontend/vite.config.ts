import path from "path"

import tailwindcss from "@tailwindcss/vite"
import { tanstackRouter } from "@tanstack/router-plugin/vite"
import react from "@vitejs/plugin-react"
import { defineConfig } from "vite"

// https://vite.dev/config/
export default defineConfig({
  plugins: [
    tanstackRouter({
      target: "react",
      autoCodeSplitting: true,
    }),
    react(),
    tailwindcss(),
  ],
  resolve: {
    alias: {
      "@": path.resolve(__dirname, "./src"),
    },
  },
  build: {
    chunkSizeWarningLimit: 2048,
  },
  server: {
    proxy: {
      // The merged claw binary serves /api/* and the WebUI WebSocket on the
      // same port as the gateway (cfg.Gateway.Port, default 18790).
      "/api": {
        target: "http://localhost:18790",
        changeOrigin: true,
      },
      "/webui": {
        target: "ws://localhost:18790",
        ws: true,
      },
    },
  },
})
