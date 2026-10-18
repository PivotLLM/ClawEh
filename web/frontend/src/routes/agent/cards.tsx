import { createFileRoute } from "@tanstack/react-router"

import { CardsPage } from "@/components/cards/cards-page"

export const Route = createFileRoute("/agent/cards")({
  component: CardsPage,
})
