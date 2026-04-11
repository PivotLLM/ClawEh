import { createFileRoute } from "@tanstack/react-router"

import { ChannelConfigPage } from "@/components/channels/channel-config-page"
import { TelegramBotsPage } from "@/components/channels/telegram-bots-page"

export const Route = createFileRoute("/channels/$name")({
  component: ChannelsByNameRoute,
})

function ChannelsByNameRoute() {
  const { name } = Route.useParams()

  if (name === "telegram") {
    return <TelegramBotsPage />
  }

  return <ChannelConfigPage channelName={name} />
}
