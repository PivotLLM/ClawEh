import { createFileRoute } from "@tanstack/react-router"

import { StatusPage } from "@/components/status/status-page"

export const Route = createFileRoute("/status")({
  component: StatusPage,
})
