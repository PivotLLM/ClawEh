import {
  ALL_SKILLS,
  type SkillInfo,
  skillsSummary,
} from "@/components/agents/agent-model"
import { Button } from "@/components/ui/button"

export interface SkillsSelectProps {
  /** The agent's skills list ("*" = every skill); [] = none. */
  selected: string[]
  availableSkills: SkillInfo[]
  onChange: (skills: string[]) => void
}

export function SkillsSelect({
  selected,
  availableSkills,
  onChange,
}: SkillsSelectProps) {
  const all = selected.includes(ALL_SKILLS)
  // With "*" every skill is shown active; the first change writes the
  // installed skills out explicitly.
  const current = all ? availableSkills.map((s) => s.name) : selected
  return (
    <div className="space-y-2">
      <div className="flex items-center gap-1.5">
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onChange([ALL_SKILLS])}
          className="h-6 px-2 text-xs"
        >
          All skills
        </Button>
        <Button
          type="button"
          variant="outline"
          size="sm"
          onClick={() => onChange([])}
          className="h-6 px-2 text-xs"
        >
          Clear
        </Button>
      </div>
      <div className="flex flex-wrap gap-1.5">
        {availableSkills.map((skill) => {
          const active = current.includes(skill.name)
          return (
            <button
              key={skill.name}
              type="button"
              onClick={() => {
                if (active) {
                  onChange(current.filter((s) => s !== skill.name))
                } else {
                  onChange([...current, skill.name])
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
          {skillsSummary(selected)}
        </p>
      )}
    </div>
  )
}
