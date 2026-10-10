import { agentIdProblem } from "@/components/agents/agent-model"
import { Input } from "@/components/ui/input"

// AgentIdField is the new-agent id input. An id the server would refuse is
// marked as the user types, with the same sentence the server gives.
export function AgentIdField({
  value,
  onChange,
  onKeyDown,
}: {
  value: string
  onChange: (value: string) => void
  onKeyDown?: (e: React.KeyboardEvent) => void
}) {
  const problem = value === "" ? null : agentIdProblem(value)
  return (
    <div className="space-y-1">
      <Input
        value={value}
        onChange={(e) => onChange(e.target.value)}
        onKeyDown={onKeyDown}
        placeholder="Agent ID (e.g. alice)"
        aria-label="Agent ID"
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
