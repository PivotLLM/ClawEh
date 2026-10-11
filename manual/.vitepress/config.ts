import { defineConfig } from "vitepress"

// The manual is embedded in the claw binary and served at /manual/ behind the
// WebUI login (web/backend/manual.go). The same build will later back the
// public website.
export default defineConfig({
  title: "ClawEh Manual",
  description: "User manual for ClawEh",
  lang: "en-US",
  base: "/manual/",
  // Output goes straight into the Go embed source.
  outDir: "../web/backend/manual",
  // Pages are served as plain files (guide/intro.html); the Go handler does
  // no extension-less rewriting.
  cleanUrls: false,
  lastUpdated: false,
  themeConfig: {
    search: { provider: "local" },
    // Nested groups: an item with `items` is a group, and groups nest.
    sidebar: [
      {
        text: "Getting started",
        items: [{ text: "Introduction", link: "/guide/introduction" }],
      },
    ],
  },
})
