import { createFileRoute } from "@tanstack/react-router"

import { MCPLayout } from "@/components/mcp/mcp-layout"

export const Route = createFileRoute("/mcp")({
  component: MCPLayout,
})
