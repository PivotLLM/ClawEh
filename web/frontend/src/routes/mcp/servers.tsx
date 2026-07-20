import { createFileRoute } from "@tanstack/react-router"

import { MCPServersPage } from "@/components/mcp/mcp-servers-page"

export const Route = createFileRoute("/mcp/servers")({
  component: MCPServersPage,
})
