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
      // import.meta.dirname, not __dirname: vite 8 warns that __dirname is
      // unsupported by the native config loader, which becomes the default in a
      // future major.
      "@": path.resolve(import.meta.dirname, "./src"),
    },
  },
  build: {
    chunkSizeWarningLimit: 500,
    rolldownOptions: {
      output: {
        codeSplitting: {
          groups: [
            {
              name: "react",
              test: /node_modules[\\/](react|react-dom|scheduler)[\\/]/,
            },
            { name: "tanstack", test: /node_modules[\\/]@tanstack[\\/]/ },
            {
              name: "radix",
              test: /node_modules[\\/](radix-ui|@radix-ui)[\\/]/,
            },
            {
              name: "markdown",
              test: /node_modules[\\/](react-markdown|remark-.*|micromark.*|mdast-.*|hast-.*|unified|unist-.*|vfile.*|character-entities.*|decode-named-character-reference|property-information|space-separated-tokens|comma-separated-tokens|html-url-attributes|trim-lines|zwitch|longest-streak|ccount|markdown-table|escape-string-regexp|bail|is-plain-obj|trough|devlop|estree-util-is-identifier-name)[\\/]/,
            },
            {
              name: "i18n",
              test: /node_modules[\\/](i18next|react-i18next|i18next-browser-languagedetector|void-elements|html-parse-stringify)[\\/]/,
            },
          ],
        },
      },
    },
  },
  test: {
    // jsdom, not node: the modules under test reach for localStorage, crypto
    // and WebSocket, and the point of the tests is that they behave correctly
    // in a browser — including when those APIs are missing or throw.
    environment: "jsdom",
    include: ["src/**/*.test.ts", "src/**/*.test.tsx"],
    restoreMocks: true,
    // Fails a test on any console.error/warn — see src/test-setup.ts. React
    // reports most real problems that way and then carries on, so without it a
    // test can pass while the component under test is complaining.
    setupFiles: ["./src/test-setup.ts"],
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
