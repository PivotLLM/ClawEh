import { createFileRoute, redirect } from "@tanstack/react-router"

import { getModels } from "@/api/models"
import { ChatPage } from "@/components/chat/chat-page"
import { SETUP_DISMISSED_KEY } from "@/components/setup/dismissed"

export const Route = createFileRoute("/")({
  // First-run convenience: send installs with no usable model straight to the
  // setup wizard. Suppressed once the user has cancelled the wizard this session
  // (so cancelling doesn't bounce them right back), and never blocks chat on a
  // transient API error.
  beforeLoad: async () => {
    try {
      if (sessionStorage.getItem(SETUP_DISMISSED_KEY)) return
    } catch {
      // sessionStorage unavailable — fall through and check models.
    }
    let configured = true
    try {
      const data = await getModels()
      configured = data.models.some((m) => m.configured)
    } catch {
      configured = true // don't redirect on a fetch error
    }
    if (!configured) throw redirect({ to: "/setup" })
  },
  component: ChatPage,
})
