import { useParams } from "@tanstack/react-router"

import { ChannelConfigPage } from "@/components/channels/channel-config-page"
import { SecMsgPage } from "@/components/channels/secmsg-page"
import { TelegramBotsPage } from "@/components/channels/telegram-bots-page"

export function ChannelPage() {
  const { name } = useParams({ from: "/channels/$name" })

  if (name === "telegram") {
    return <TelegramBotsPage />
  }

  if (name === "secmsg") {
    return <SecMsgPage />
  }

  return <ChannelConfigPage channelName={name} />
}
