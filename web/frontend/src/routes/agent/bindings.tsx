import { createFileRoute } from "@tanstack/react-router"

import { BindingsPage } from "@/components/bindings/bindings-page"

export const Route = createFileRoute("/agent/bindings")({
  component: BindingsPage,
})
