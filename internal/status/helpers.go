package status

import (
	"fmt"
	"os"

	"github.com/PivotLLM/ClawEh/app"
	"github.com/PivotLLM/ClawEh/internal"
	"github.com/PivotLLM/ClawEh/internal/pidfile"
)

func statusCmd() {
	cfg, err := internal.LoadConfig()
	if err != nil {
		fmt.Printf("Error loading config: %v\n", err)
		return
	}

	configPath := internal.GetConfigPath()

	fmt.Println("Status")
	fmt.Printf("Version: %s\n", app.Version())
	build, _ := app.BuildInfo()
	if build != "" {
		fmt.Printf("Build: %s\n", build)
	}
	fmt.Println()

	printGateway(cfg.DataDir())
	fmt.Println()

	if _, err := os.Stat(configPath); err == nil {
		fmt.Println("Config:", configPath, "✓")
	} else {
		fmt.Println("Config:", configPath, "✗")
	}

	workspace := cfg.WorkspacePath()
	if _, err := os.Stat(workspace); err == nil {
		fmt.Println("Workspace:", workspace, "✓")
	} else {
		fmt.Println("Workspace:", workspace, "✗")
	}

	if _, err := os.Stat(configPath); err == nil {
		fmt.Printf("Model: %s\n", cfg.Agents.Defaults.DefaultModelName())

		// Report each configured provider and whether it carries credentials.
		fmt.Printf("\nProviders (%d):\n", len(cfg.Providers))
		for i := range cfg.Providers {
			p := &cfg.Providers[i]
			credentialed := p.APIKey != "" || p.BaseURL != ""
			mark := "not set"
			if credentialed {
				mark = "✓"
			}
			detail := p.Protocol
			if p.BaseURL != "" {
				detail = fmt.Sprintf("%s · %s", p.Protocol, p.BaseURL)
			}
			fmt.Printf("  %-16s %s (%s)\n", p.Name+":", mark, detail)
		}
	}
}

// printGateway reports whether the gateway for this data directory is running,
// and what it is costing in RAM.
//
// Until now this command could not answer either question: it reads config from
// disk and never looks at the process, so "status" described an installation
// rather than a running system.
//
// The instance is found through the PID file in the data directory, which is
// what makes the answer specific — one binary runs several gateways on a host,
// and this command already resolved CLAW_HOME to find the config, so it reports
// on the instance the caller is actually asking about.
func printGateway(dataDir string) {
	pid, running := pidfile.Read(dataDir)
	if !running {
		// A stale file left by a kill -9 reads as not running, which is the
		// truth; saying so beats reporting a dead pid's memory.
		fmt.Println("Gateway:         not running")
		return
	}

	line := fmt.Sprintf("Gateway:         running (pid %d)", pid)
	if rss, ok := pidfile.RSSBytes(pid); ok {
		// Resident set size: the physical RAM the process occupies. Not VmSize,
		// which for a Go process counts over a gigabyte of reserved address
		// space and would make the gateway look enormous when it is not.
		line += fmt.Sprintf(", %s RAM", humanBytes(rss))
	}
	fmt.Println(line)
}

// humanBytes renders a byte count in the unit a person would use to quote it.
func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit && exp < 3; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
