import { createFileRoute } from "@tanstack/react-router"

import { AgentLayout } from "@/components/agents/agent-layout"

export const Route = createFileRoute("/agent")({
  component: AgentLayout,
})
