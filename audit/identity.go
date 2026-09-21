// ClawEh
// License: MIT

package audit

import (
	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/config"
)

// timeFormat is the local time, with zone, used everywhere in the report.
const timeFormat = "2006-01-02 15:04:05 MST"

func collectIdentity(_ *config.Config, env Environment) Section {
	return Section{
		Title: "Identity",
		Tables: []Table{pairs("",
			row("Product", app.Name()),
			row("Tag line", app.TagLine()),
			row("Version", orValue(env.Version, app.Version())),
			row("Commit", orValue(env.Commit, unknown)),
			row("Build time", orValue(env.BuildTime, unknown)),
			row("Go toolchain", orValue(env.GoVersion, unknown)),
			row("OS / architecture", orValue(env.OS, unknown)+" / "+orValue(env.Arch, unknown)),
			row("Hostname", orValue(env.Hostname, unknown)),
			row("Executable", orValue(env.Executable, unknown)),
			row("Config file", orValue(env.ConfigPath, unknown)),
			row("Data directory", orValue(env.DataDir, unknown)),
			row("Runs as", "user "+orValue(env.User, unknown)+", group "+orValue(env.Group, unknown)),
			row("Generated", env.Now.Format(timeFormat)),
		)},
	}
}
