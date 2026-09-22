import { createFileRoute } from "@tanstack/react-router"

import { ReportPage } from "@/components/report/report-page"

export const Route = createFileRoute("/report")({
  component: ReportPage,
})
