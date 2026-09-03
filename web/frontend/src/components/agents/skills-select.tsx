import { type SkillInfo } from "@/components/agents/agent-model"

export interface SkillsSelectProps {
  selected: string[]
  availableSkills: SkillInfo[]
  onChange: (skills: string[]) => void
}

export function SkillsSelect({
  selected,
  availableSkills,
  onChange,
}: SkillsSelectProps) {
  const isAllSelected = selected.length === 0
  return (
    <div className="space-y-2">
      <div className="flex flex-wrap gap-1.5">
        {availableSkills.map((skill) => {
          const active = selected.includes(skill.name)
          return (
            <button
              key={skill.name}
              type="button"
              onClick={() => {
                if (active) {
                  onChange(selected.filter((s) => s !== skill.name))
                } else {
                  onChange([...selected, skill.name])
                }
              }}
              className={[
                "cursor-pointer rounded-md border px-2 py-0.5 text-xs font-medium transition-colors",
                active
                  ? "border-primary/50 bg-secondary text-foreground"
                  : "border-border/50 text-muted-foreground hover:border-border hover:text-foreground bg-transparent",
              ].join(" ")}
              title={skill.description}
            >
              {skill.name}
            </button>
          )
        })}
        {availableSkills.length === 0 && (
          <span className="text-muted-foreground text-xs">
            No skills installed
          </span>
        )}
      </div>
      {availableSkills.length > 0 && (
        <p className="text-muted-foreground text-xs">
          {isAllSelected
            ? "No skills selected (agent has no skill access)"
            : `${selected.length} skill${selected.length === 1 ? "" : "s"} selected`}
        </p>
      )}
    </div>
  )
}
