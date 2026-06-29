import { createFileRoute } from "@tanstack/react-router"

import { SetupWizard } from "@/components/setup/setup-wizard"

export const Route = createFileRoute("/setup")({
  component: SetupWizard,
})
