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
  // Pages are served as plain files (getting-started/overview.html); the Go handler does
  // no extension-less rewriting.
  cleanUrls: false,
  lastUpdated: false,
  themeConfig: {
    search: { provider: "local" },
    nav: [
      { text: "Home", link: "/" },
    ],
    // Nested collapsible groups; a group's title links to its landing page.
    sidebar: [
      {
        text: "Getting started",
        link: "/getting-started/",
        collapsed: false,
        items: [
          { text: "Overview", link: "/getting-started/overview" },
          { text: "Installing", link: "/getting-started/install" },
          { text: "Quick start", link: "/getting-started/quickstart" },
          { text: "A tour of the WebUI", link: "/getting-started/webui-tour" },
          { text: "Upgrading", link: "/getting-started/upgrading" },
        ],
      },
      {
        text: "Providers and models",
        link: "/providers/",
        collapsed: true,
        items: [
          { text: "Providers, models and fallback", link: "/providers/concepts" },
          { text: "Cloud APIs", link: "/providers/cloud-apis" },
          { text: "OpenRouter", link: "/providers/openrouter" },
          { text: "Amazon Bedrock", link: "/providers/bedrock" },
          { text: "Local models", link: "/providers/local-models" },
          { text: "CLI providers", link: "/providers/cli-providers" },
        ],
      },
      {
        text: "Agents",
        link: "/agents/",
        collapsed: true,
        items: [
          { text: "Creating and editing agents", link: "/agents/creating" },
          { text: "Tools and skills", link: "/agents/tools-and-skills" },
          { text: "Web search and fetch", link: "/agents/web-search" },
          { text: "Conversation and memory", link: "/agents/memory" },
          { text: "Files and mounts", link: "/agents/files-and-mounts" },
          { text: "Messages between agents", link: "/agents/messaging" },
          { text: "Human agents", link: "/agents/human-agents" },
          { text: "Character cards", link: "/agents/character-cards" },
          { text: "Sub-agents", link: "/agents/subagents" },
          { text: "Scheduled jobs", link: "/agents/scheduling" },
        ],
      },
      {
        text: "Channels",
        link: "/channels/",
        collapsed: true,
        items: [
          { text: "WebUI chat", link: "/channels/webui-chat" },
          { text: "Telegram", link: "/channels/telegram" },
          { text: "Slack", link: "/channels/slack" },
          { text: "Discord", link: "/channels/discord" },
          { text: "Matrix", link: "/channels/matrix" },
          { text: "SecMsg (Signal)", link: "/channels/secmsg" },
          { text: "Devices", link: "/channels/devices" },
          { text: "Routing and bindings", link: "/channels/routing" },
          { text: "Message API", link: "/channels/message-api" },
          { text: "Voice messages and transcription", link: "/channels/voice" },
        ],
      },
      {
        text: "Forums",
        link: "/forums/",
        collapsed: true,
        items: [
          { text: "What a forum is", link: "/forums/" },
          { text: "Templates", link: "/forums/templates" },
          { text: "Running a forum", link: "/forums/running" },
          { text: "Results", link: "/forums/results" },
        ],
      },
      { text: "Maestro", link: "/maestro" },
      { text: "MCPFusion", link: "/mcpfusion" },
      { text: "MCP", link: "/mcp" },
      {
        text: "Security and administration",
        link: "/admin/",
        collapsed: true,
        items: [
          { text: "Signing in", link: "/admin/login" },
          { text: "HTTPS and certificates", link: "/admin/https" },
          { text: "Network access", link: "/admin/network" },
          { text: "Remote access", link: "/admin/remote-access" },
          { text: "Check Up", link: "/admin/checkup" },
          { text: "Audit log", link: "/admin/audit" },
          { text: "Alerts", link: "/admin/alerts" },
          { text: "Backup and restore", link: "/admin/backup" },
          { text: "Logs", link: "/admin/logs" },
        ],
      },
      {
        text: "Reference",
        link: "/reference/",
        collapsed: true,
        items: [
          { text: "Configuration file", link: "/reference/configuration" },
          { text: "Command line", link: "/reference/cli" },
          { text: "Chat commands", link: "/reference/chat-commands" },
          { text: "Troubleshooting", link: "/reference/troubleshooting" },
          { text: "Glossary", link: "/reference/glossary" },
        ],
      },
    ],
  },
})
