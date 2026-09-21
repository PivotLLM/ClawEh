// ClawEh
// License: MIT

package audit

import (
	"context"
	"time"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
)

// collector builds one section of the report from the config and environment.
type collector func(ctx context.Context, cfg *config.Config, env Environment) Section

// collectors lists the report sections in order. The Summary is rendered
// second but computed last, from the same inputs, so it is not in this list.
// Adding or dropping a section is one file and one line here.
var collectors = []collector{
	collectIdentity,
	collectNetwork,
	collectProviders,
	collectCredentials,
	collectChannels,
	collectAgents,
	collectExternal,
	collectDevices,
	collectData,
	collectScheduled,
}

// Collect builds the report. It never fails: a store that is missing or
// unreadable becomes an "unavailable: <reason>" row in its section.
func Collect(ctx context.Context, cfg *config.Config, env Environment) *Report {
	if cfg == nil {
		cfg = &config.Config{}
	}
	now := env.Now
	if now.IsZero() {
		now = time.Now()
	}
	env.Now = now
	r := &Report{
		Product:     app.Name(),
		TagLine:     app.TagLine(),
		Version:     orValue(env.Version, app.Version()),
		GeneratedAt: now,
	}
	sections := make([]Section, 0, len(collectors)+1)
	for _, c := range collectors {
		sections = append(sections, c(ctx, cfg, env))
	}
	summary := collectSummary(ctx, cfg, env)
	r.Sections = append(r.Sections, sections[0], summary)
	r.Sections = append(r.Sections, sections[1:]...)
	return r
}
