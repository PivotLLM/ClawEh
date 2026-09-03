import path from "path"

import tailwindcss from "@tailwindcss/vite"
import { tanstackRouter } from "@tanstack/router-plugin/vite"
import react from "@vitejs/plugin-react"
// defineConfig from vitest/config, not vite: it is the same function widened
// to accept the `test` block below. Importing it from "vite" makes tsc reject
// that block as an unknown property.
import { defineConfig } from "vitest/config"

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
  test: {
    // jsdom, not node: the modules under test reach for localStorage, crypto
    // and WebSocket, and the point of the tests is that they behave correctly
    // in a browser — including when those APIs are missing or throw.
    environment: "jsdom",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
    restoreMocks: true,
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
