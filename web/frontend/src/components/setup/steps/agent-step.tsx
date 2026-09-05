import { useTranslation } from "react-i18next"

import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"

interface AgentStepProps {
  agentName: string
  setAgentName: (v: string) => void
}

export function AgentStep({ agentName, setAgentName }: AgentStepProps) {
  const { t } = useTranslation()

  return (
    <div className="space-y-5">
      <div className="space-y-1">
        <h1 className="text-xl font-semibold">{t("setup.agent.title")}</h1>
        <p className="text-muted-foreground text-sm">{t("setup.agent.body")}</p>
      </div>
      <div className="space-y-2">
        <Label>{t("setup.agent.nameLabel")}</Label>
        <Input
          value={agentName}
          onChange={(e) => setAgentName(e.target.value)}
        />
      </div>
    </div>
  )
}
