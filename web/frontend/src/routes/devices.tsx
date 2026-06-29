import { createFileRoute } from "@tanstack/react-router"

import { DevicesPage } from "@/components/devices/devices-page"

export const Route = createFileRoute("/devices")({
  component: DevicesPage,
})
