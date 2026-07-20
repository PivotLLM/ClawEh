import { createFileRoute } from "@tanstack/react-router"

import { MCPConfigPage } from "@/components/mcp/mcp-page"

export const Route = createFileRoute("/mcp/config")({
  component: MCPConfigPage,
})
