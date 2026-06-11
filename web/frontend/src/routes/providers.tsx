import { createFileRoute } from "@tanstack/react-router"

import { ProvidersPage } from "@/components/providers/providers-page"

export const Route = createFileRoute("/providers")({
  component: ProvidersPage,
})
