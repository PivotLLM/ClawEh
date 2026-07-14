import { createFileRoute } from "@tanstack/react-router"

import { ChannelConfigPage } from "@/components/channels/channel-config-page"
import { SecMsgPage } from "@/components/channels/secmsg-page"
import { TelegramBotsPage } from "@/components/channels/telegram-bots-page"

export const Route = createFileRoute("/channels/$name")({
  component: ChannelsByNameRoute,
})

function ChannelsByNameRoute() {
  const { name } = Route.useParams()

  if (name === "telegram") {
    return <TelegramBotsPage />
  }

  if (name === "secmsg") {
    return <SecMsgPage />
  }

  return <ChannelConfigPage channelName={name} />
}
