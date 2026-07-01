import { createFileRoute } from "@tanstack/react-router"

import { VoicePage } from "@/components/voice/voice-page"

export const Route = createFileRoute("/voice")({
  component: VoicePage,
})
