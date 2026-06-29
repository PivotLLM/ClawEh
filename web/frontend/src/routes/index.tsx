import { createFileRoute, redirect } from "@tanstack/react-router"

import { getSetupStatus } from "@/api/system"
import { ChatPage } from "@/components/chat/chat-page"
import { SETUP_DISMISSED_KEY } from "@/components/setup/dismissed"

export const Route = createFileRoute("/")({
  // First-run convenience: send a fresh, never-saved install straight to the
  // setup wizard. Suppressed once the user has cancelled the wizard this session
  // (so cancelling doesn't bounce them right back), and never blocks chat on a
  // transient API error.
  beforeLoad: async () => {
    try {
      if (sessionStorage.getItem(SETUP_DISMISSED_KEY)) return
    } catch {
      // sessionStorage unavailable — fall through and check setup status.
    }
    let needsSetup = false
    try {
      needsSetup = (await getSetupStatus()).needs_setup
    } catch {
      needsSetup = false // don't redirect on a fetch error
    }
    if (needsSetup) throw redirect({ to: "/setup" })
  },
  component: ChatPage,
})
