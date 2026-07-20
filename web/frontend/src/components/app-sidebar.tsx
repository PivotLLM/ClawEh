import { IconChevronRight } from "@tabler/icons-react"
import {
  IconArrowsTransferDown,
  IconAtom,
  IconBrain,
  IconDeviceMobile,
  IconListDetails,
  IconMessageCircle,
  IconMicrophone,
  IconPlugConnected,
  IconRoute,
  IconRobot,
  IconSettings,
  IconSparkles,
  IconTools,
} from "@tabler/icons-react"
import { Link, useRouterState } from "@tanstack/react-router"
import * as React from "react"
import { useTranslation } from "react-i18next"

import {
  Collapsible,
  CollapsibleContent,
  CollapsibleTrigger,
} from "@/components/ui/collapsible"
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarFooter,
  SidebarRail,
} from "@/components/ui/sidebar"
import { getVersion } from "@/api/system"
import { useSidebarChannels } from "@/hooks/use-sidebar-channels"

interface NavSubItem {
  title: string
  url: string
  translateTitle?: boolean
}

interface NavItem {
  title: string
  url: string
  icon: React.ComponentType<{ className?: string }>
  translateTitle?: boolean
  // When present, the item renders as a nested collapsible whose children are
  // sub-links (e.g. MCP → Config / Servers). `url` is used only to compute the
  // active/expanded state, not as a link.
  children?: NavSubItem[]
}

interface NavGroup {
  label: string
  defaultOpen: boolean
  items: NavItem[]
}

const baseNavGroups: Omit<NavGroup, "items">[] = [
  {
    label: "navigation.chat",
    defaultOpen: false,
  },
  {
    label: "navigation.model_group",
    defaultOpen: false,
  },
  {
    label: "navigation.agent_group",
    defaultOpen: false,
  },
  {
    label: "navigation.services",
    defaultOpen: false,
  },
]

export function AppSidebar({ ...props }: React.ComponentProps<typeof Sidebar>) {
  const routerState = useRouterState()
  const { t } = useTranslation()
  const currentPath = routerState.location.pathname
  const { channelItems } = useSidebarChannels({ t })

  // ClawEh build version, shown in the sidebar footer. Static for the process
  // lifetime, so fetch once; failures leave the label as just "ClawEh".
  const [version, setVersion] = React.useState("")
  React.useEffect(() => {
    getVersion()
      .then(setVersion)
      .catch(() => {})
  }, [])

  const navGroups: NavGroup[] = React.useMemo(() => {
    return [
      {
        ...baseNavGroups[0],
        items: [
          {
            title: "navigation.chat",
            url: "/",
            icon: IconMessageCircle,
            translateTitle: true,
          },
        ],
      },
      {
        ...baseNavGroups[2],
        items: [
          {
            title: "navigation.agents",
            url: "/agents",
            icon: IconRobot,
            translateTitle: true,
          },
          {
            title: "navigation.bindings",
            url: "/agent/bindings",
            icon: IconArrowsTransferDown,
            translateTitle: true,
          },
          {
            title: "navigation.memory",
            url: "/memory",
            icon: IconBrain,
            translateTitle: true,
          },
          {
            title: "navigation.skills",
            url: "/agent/skills",
            icon: IconSparkles,
            translateTitle: true,
          },
          {
            title: "navigation.tools",
            url: "/agent/tools",
            icon: IconTools,
            translateTitle: true,
          },
        ],
      },
      {
        ...baseNavGroups[1],
        items: [
          {
            title: "navigation.providers",
            url: "/providers",
            icon: IconRoute,
            translateTitle: true,
          },
          {
            title: "navigation.models",
            url: "/models",
            icon: IconAtom,
            translateTitle: true,
          },
        ],
      },
      {
        label: "navigation.channels_group",
        defaultOpen: false,
        items: channelItems
          .map((item) => ({
            title: item.title,
            url: item.url,
            icon: item.icon,
            translateTitle: false,
          }))
          .sort((a, b) => a.title.localeCompare(b.title)),
      },
      {
        ...baseNavGroups[3],
        items: [
          {
            title: "Devices",
            url: "/devices",
            icon: IconDeviceMobile,
            translateTitle: false,
          },
          {
            title: "Speech",
            url: "/voice",
            icon: IconMicrophone,
            translateTitle: false,
          },
          {
            title: "navigation.config",
            url: "/config",
            icon: IconSettings,
            translateTitle: true,
          },
          {
            title: "navigation.mcp",
            url: "/mcp",
            icon: IconPlugConnected,
            translateTitle: true,
            children: [
              {
                title: "navigation.mcp_config",
                url: "/mcp/config",
                translateTitle: true,
              },
              {
                title: "navigation.mcp_servers",
                url: "/mcp/servers",
                translateTitle: true,
              },
            ],
          },
          {
            title: "navigation.logs",
            url: "/logs",
            icon: IconListDetails,
            translateTitle: true,
          },
        ],
      },
    ]
  }, [channelItems])

  return (
    <Sidebar
      {...props}
      className="bg-background border-r-border/20 border-r pt-3"
    >
      <SidebarContent className="bg-background">
        {navGroups.map((group) => (
          <Collapsible
            key={group.label}
            defaultOpen={group.defaultOpen}
            className="group/collapsible mb-1"
          >
            <SidebarGroup className="px-2 py-0">
              <SidebarGroupLabel asChild>
                <CollapsibleTrigger className="hover:bg-muted/60 flex w-full cursor-pointer items-center justify-between rounded-md px-2 py-1.5 transition-colors">
                  <span className="text-sm">{t(group.label)}</span>
                  <IconChevronRight className="size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/collapsible:rotate-90" />
                </CollapsibleTrigger>
              </SidebarGroupLabel>
              <CollapsibleContent>
                <SidebarGroupContent className="pt-1">
                  <SidebarMenu>
                    {group.items.map((item) => {
                      const isActive =
                        currentPath === item.url ||
                        (item.url !== "/" &&
                          currentPath.startsWith(`${item.url}/`))

                      // Items with children render as a nested collapsible whose
                      // sub-links are the actual navigation targets. The parent is
                      // a toggle, not a link.
                      if (item.children && item.children.length > 0) {
                        return (
                          <Collapsible
                            key={item.title}
                            defaultOpen={isActive}
                            className="group/sub"
                          >
                            <SidebarMenuItem>
                              <SidebarMenuButton
                                asChild
                                isActive={isActive}
                                className={`h-9 px-3 ${isActive ? "text-foreground font-medium" : "text-muted-foreground hover:bg-muted/60"}`}
                              >
                                <CollapsibleTrigger className="w-full cursor-pointer">
                                  <item.icon
                                    className={`size-4 ${isActive ? "opacity-100" : "opacity-60"}`}
                                  />
                                  <span
                                    className={
                                      isActive ? "opacity-100" : "opacity-80"
                                    }
                                  >
                                    {item.translateTitle === false
                                      ? item.title
                                      : t(item.title)}
                                  </span>
                                  <IconChevronRight className="ml-auto size-3.5 opacity-50 transition-transform duration-200 group-data-[state=open]/sub:rotate-90" />
                                </CollapsibleTrigger>
                              </SidebarMenuButton>
                            </SidebarMenuItem>
                            <CollapsibleContent>
                              <SidebarMenu className="border-border/40 ml-5 border-l pl-1">
                                {item.children.map((child) => {
                                  const childActive = currentPath === child.url
                                  return (
                                    <SidebarMenuItem key={child.title}>
                                      <SidebarMenuButton
                                        asChild
                                        isActive={childActive}
                                        className={`h-8 px-3 text-sm ${childActive ? "bg-accent/80 text-foreground font-medium" : "text-muted-foreground hover:bg-muted/60"}`}
                                      >
                                        <Link to={child.url}>
                                          <span
                                            className={
                                              childActive
                                                ? "opacity-100"
                                                : "opacity-80"
                                            }
                                          >
                                            {child.translateTitle === false
                                              ? child.title
                                              : t(child.title)}
                                          </span>
                                        </Link>
                                      </SidebarMenuButton>
                                    </SidebarMenuItem>
                                  )
                                })}
                              </SidebarMenu>
                            </CollapsibleContent>
                          </Collapsible>
                        )
                      }

                      return (
                        <SidebarMenuItem key={item.title}>
                          <SidebarMenuButton
                            asChild
                            isActive={isActive}
                            className={`h-9 px-3 ${isActive ? "bg-accent/80 text-foreground font-medium" : "text-muted-foreground hover:bg-muted/60"}`}
                          >
                            <Link to={item.url}>
                              <item.icon
                                className={`size-4 ${isActive ? "opacity-100" : "opacity-60"}`}
                              />
                              <span
                                className={
                                  isActive ? "opacity-100" : "opacity-80"
                                }
                              >
                                {item.translateTitle === false
                                  ? item.title
                                  : t(item.title)}
                              </span>
                            </Link>
                          </SidebarMenuButton>
                        </SidebarMenuItem>
                      )
                    })}
                  </SidebarMenu>
                </SidebarGroupContent>
              </CollapsibleContent>
            </SidebarGroup>
          </Collapsible>
        ))}
      </SidebarContent>
      <SidebarFooter className="text-muted-foreground px-3 py-2 text-xs">
        ClawEh{version ? ` v${version}` : ""}
      </SidebarFooter>
      <SidebarRail />
    </Sidebar>
  )
}
