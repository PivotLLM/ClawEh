import { useTranslation } from "react-i18next"

import { newAgentIdProblem } from "@/components/agents/agent-model"
import { Input } from "@/components/ui/input"

// AgentIdField is the new-agent id input. An id the server would refuse,
// including one of existingIds, is marked as the user types, with the same
// sentence the server gives.
export function AgentIdField({
  value,
  existingIds,
  onChange,
  onKeyDown,
}: {
  value: string
  existingIds: readonly string[]
  onChange: (value: string) => void
  onKeyDown?: (e: React.KeyboardEvent) => void
}) {
  const { t } = useTranslation()
  const problem = value === "" ? null : newAgentIdProblem(value, existingIds)
  return (
    <div className="space-y-1">
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder={t("agents.idPlaceholder")}
        aria-label={t("agents.idLabel")}
        aria-invalid={problem !== null}
        aria-describedby={problem ? "agent-id-problem" : undefined}
        // Deliberate: the form only exists because the user just activated
        // Add Agent, so focus belongs in its first field; without it a
        // keyboard user is left on the page body and has to Tab back to find
        // the form.
        // oxlint-disable-next-line no-autofocus
        autoFocus
      />
      {problem && (
        <p id="agent-id-problem" className="text-destructive text-xs">
          {problem}
        </p>
      )}
    </div>
  )
}
