import {
  IconBrandChrome,
  IconBrandDiscord,
  IconBrandLine,
  IconBrandMatrix,
  IconBrandSlack,
  IconBrandTelegram,
  IconPlug,
  IconShieldLock,
} from "@tabler/icons-react"
import type { TFunction } from "i18next"
import * as React from "react"

import {
  type AppConfig,
  type SupportedChannel,
  getAppConfig,
  getChannelsCatalog,
} from "@/api/channels"
import { getChannelDisplayName } from "@/components/channels/channel-display-name"

const CHANNEL_IMPORTANCE_ORDER = [
  "discord",
  "telegram",
  "secmsg",
  "slack",
  "line",
  "matrix",
  "webui",
]
const CHANNEL_IMPORTANCE_INDEX = new Map(
  CHANNEL_IMPORTANCE_ORDER.map((name, index) => [name, index]),
)

const CHANNEL_ICON_MAP: Record<
  string,
  React.ComponentType<{ className?: string }>
> = {
  telegram: IconBrandTelegram,
  secmsg: IconShieldLock,
  discord: IconBrandDiscord,
  slack: IconBrandSlack,
  line: IconBrandLine,
  matrix: IconBrandMatrix,
  webui: IconBrandChrome,
}

function asRecord(value: unknown): Record<string, unknown> {
  if (value && typeof value === "object" && !Array.isArray(value)) {
    return value as Record<string, unknown>
  }
  return {}
}

function isChannelEnabled(
  channel: SupportedChannel,
  channelsConfig: Record<string, unknown>,
): boolean {
  const channelConfig = asRecord(channelsConfig[channel.config_key])
  return channelConfig.enabled === true
}

function buildChannelEnabledMap(
  channels: SupportedChannel[],
  appConfig: AppConfig,
): Record<string, boolean> {
  const channelsConfig = asRecord(asRecord(appConfig).channels)
  const result: Record<string, boolean> = {}
  for (const channel of channels) {
    result[channel.name] = isChannelEnabled(channel, channelsConfig)
  }
  return result
}

export interface SidebarChannelNavItem {
  key: string
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
}

interface UseSidebarChannelsOptions {
  t: TFunction
}

export function useSidebarChannels({ t }: UseSidebarChannelsOptions) {
  const [channels, setChannels] = React.useState<SupportedChannel[]>([])
  const [enabledMap, setEnabledMap] = React.useState<Record<string, boolean>>(
    {},
  )

  const reloadChannels = React.useCallback((shouldApply?: () => boolean) => {
    Promise.all([
      getChannelsCatalog(),
      getAppConfig().catch(() => ({}) as AppConfig),
    ])
      .then(([catalog, appConfig]) => {
        if (shouldApply && !shouldApply()) {
          return
        }
        setChannels(catalog.channels)
        setEnabledMap(buildChannelEnabledMap(catalog.channels, appConfig))
      })
      .catch(() => {
        if (shouldApply && !shouldApply()) {
          return
        }
        setChannels([])
        setEnabledMap({})
      })
  }, [])

  React.useEffect(() => {
    let active = true
    reloadChannels(() => active)
    return () => {
      active = false
    }
  }, [reloadChannels])

  const sortedChannels = React.useMemo(() => {
    const list = [...channels]
    list.sort((a, b) => {
      const aEnabled = enabledMap[a.name] === true
      const bEnabled = enabledMap[b.name] === true
      if (aEnabled !== bEnabled) {
        return aEnabled ? -1 : 1
      }

      const aImportance =
        CHANNEL_IMPORTANCE_INDEX.get(a.name) ?? Number.MAX_SAFE_INTEGER
      const bImportance =
        CHANNEL_IMPORTANCE_INDEX.get(b.name) ?? Number.MAX_SAFE_INTEGER
      if (aImportance !== bImportance) {
        return aImportance - bImportance
      }

      return getChannelDisplayName(a, t).localeCompare(
        getChannelDisplayName(b, t),
      )
    })
    return list
  }, [channels, enabledMap, t])

  const channelItems = React.useMemo<SidebarChannelNavItem[]>(
    () =>
      sortedChannels.map((channel) => ({
        key: channel.name,
        title: getChannelDisplayName(channel, t),
        url: `/channels/${channel.name}`,
        icon: CHANNEL_ICON_MAP[channel.name] ?? IconPlug,
      })),
    [sortedChannels, t],
  )

  return {
    channelItems,
  }
}
