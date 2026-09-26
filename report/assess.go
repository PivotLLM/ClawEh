// ClawEh
// License: MIT

package report

import (
	"context"
	"time"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
)

// Identity is the one-line product identification shown above the assessment
// in the WebUI: what is running, which build, on what.
type Identity struct {
	Name        string    `json:"name"`
	Version     string    `json:"version"`
	Build       string    `json:"build"`
	Platform    string    `json:"platform"`
	GeneratedAt time.Time `json:"generated_at"`
}

// AssessmentRow is one row of the security assessment table. Action mirrors
// the "*" mark in the PDF's first column.
type AssessmentRow struct {
	Action bool   `json:"action"`
	Item   string `json:"item"`
	Status string `json:"status"`
}

// Assessment is the identity and the security assessment table as data, for
// callers that render the table themselves rather than the PDF.
type Assessment struct {
	Identity   Identity        `json:"identity"`
	Assessment []AssessmentRow `json:"assessment"`
}

// Assess returns the identity and the security assessment rows, in the order
// and with the text the PDF renders. Like Collect it never fails and never
// includes a secret value.
func Assess(ctx context.Context, cfg *config.Config, env Environment) Assessment {
	cfg, env = normalize(cfg, env)
	s := collectAssessment(ctx, cfg, env)
	var rows []AssessmentRow
	for _, t := range s.Tables {
		for _, r := range t.Rows {
			rows = append(rows, AssessmentRow{Action: r[0] == actionMark, Item: r[1], Status: r[2]})
		}
	}
	return Assessment{
		Identity: Identity{
			Name:        app.Name(),
			Version:     orValue(env.Version, app.Version()),
			Build:       env.BuildTime,
			Platform:    orValue(env.OS, unknown) + "/" + orValue(env.Arch, unknown) + " on " + orValue(env.Hostname, unknown),
			GeneratedAt: env.Now,
		},
		Assessment: rows,
	}
}

// normalize fills the inputs every collector may rely on: a non-nil config
// and a generation time.
func normalize(cfg *config.Config, env Environment) (*config.Config, Environment) {
	if cfg == nil {
		cfg = &config.Config{}
	}
	if env.Now.IsZero() {
		env.Now = time.Now()
	}
	return cfg, env
}
