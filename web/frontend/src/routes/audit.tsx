import { createFileRoute } from "@tanstack/react-router"

import { AuditPage } from "@/components/audit/audit-page"

export const Route = createFileRoute("/audit")({
  component: AuditPage,
})
