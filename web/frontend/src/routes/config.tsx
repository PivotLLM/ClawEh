import { createFileRoute } from "@tanstack/react-router"

import { ConfigLayout } from "@/components/config/config-layout"

export const Route = createFileRoute("/config")({
  component: ConfigLayout,
})
