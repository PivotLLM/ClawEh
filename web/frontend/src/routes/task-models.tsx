import { createFileRoute } from "@tanstack/react-router"

import { TaskModelsPage } from "@/components/task-models/task-models-page"

export const Route = createFileRoute("/task-models")({
  component: TaskModelsPage,
})
