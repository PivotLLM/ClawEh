import { createFileRoute } from "@tanstack/react-router"

import { ChannelPage } from "@/components/channels/channel-page"

export const Route = createFileRoute("/channels/$name")({
  component: ChannelPage,
})
