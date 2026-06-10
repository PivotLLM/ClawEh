package files

import (
	"regexp"

	"github.com/PivotLLM/ClawEh/pkg/config"
)

// resolveMemoryDir returns the memory_dir override for the agent, or "".
func resolveMemoryDir(agentCfg *config.AgentConfig) string {
	if agentCfg == nil {
		return ""
	}
	return agentCfg.MemoryDir
}

// compilePatterns compiles regex patterns, silently skipping invalid ones.
func compilePatterns(patterns []string) []*regexp.Regexp {
	compiled := make([]*regexp.Regexp, 0, len(patterns))
	for _, p := range patterns {
		re, err := regexp.Compile(p)
		if err != nil {
			continue
		}
		compiled = append(compiled, re)
	}
	return compiled
}
