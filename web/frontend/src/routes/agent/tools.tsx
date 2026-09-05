import { createFileRoute } from "@tanstack/react-router"

import { ToolsPage } from "@/components/tools/tools-page"

export const Route = createFileRoute("/agent/tools")({
  component: ToolsPage,
})
