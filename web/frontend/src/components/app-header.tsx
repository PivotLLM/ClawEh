import { IconBook, IconMenu2, IconMoon, IconSun } from "@tabler/icons-react"
import { Link } from "@tanstack/react-router"

import { Button } from "@/components/ui/button.tsx"
import { SidebarTrigger } from "@/components/ui/sidebar"
import { useTheme } from "@/hooks/use-theme.ts"

export function AppHeader() {
  const { theme, toggleTheme } = useTheme()

  return (
    <header className="bg-background/95 supports-backdrop-filter:bg-background/60 border-b-border/50 sticky top-0 z-50 flex h-14 shrink-0 items-center justify-between border-b px-4 backdrop-blur">
      <div className="flex items-center gap-2">
        <SidebarTrigger className="text-muted-foreground hover:bg-accent hover:text-foreground flex h-9 w-9 items-center justify-center rounded-lg sm:hidden [&>svg]:size-5">
          <IconMenu2 />
        </SidebarTrigger>
        <div className="hidden shrink-0 items-center sm:flex">
          <Link to="/">
            <img className="max-h-10 w-auto" src="/logo.png" alt="Logo" />
          </Link>
        </div>
      </div>

      <div className="text-muted-foreground flex items-center gap-1 text-sm font-medium md:gap-2">
        <Button variant="ghost" size="icon" className="size-8" asChild>
          <a
            href="https://github.com/PivotLLM/ClawEh"
            target="_blank"
            rel="noreferrer"
          >
            <IconBook className="size-4.5" />
          </a>
        </Button>

        <Button
          variant="ghost"
          size="icon"
          className="size-8"
          onClick={toggleTheme}
        >
          {theme === "dark" ? (
            <IconSun className="size-4.5" />
          ) : (
            <IconMoon className="size-4.5" />
          )}
        </Button>
      </div>
    </header>
  )
}
