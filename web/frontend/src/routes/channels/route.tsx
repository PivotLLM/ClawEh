import { createFileRoute } from "@tanstack/react-router"

import { ChannelsLayout } from "@/components/channels/channels-layout"

export const Route = createFileRoute("/channels")({
  component: ChannelsLayout,
})
