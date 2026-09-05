package commands

import (
	"context"
	"fmt"
	"strconv"
	"strings"
)

func modelCommand() Definition {
	return Definition{
		Name:        "model",
		Description: "Select which configured model to use",
		Usage:       "/model <n>",
		Handler: func(_ context.Context, req Request, rt *Runtime) error {
			if rt == nil || rt.GetAgentModels == nil || rt.SetActiveModel == nil {
				return req.Reply("This agent has no configured models; it uses the global default model.")
			}
			entries, active := rt.GetAgentModels()
			if len(entries) == 0 {
				return req.Reply("This agent has no configured models; it uses the global default model.")
			}

			arg := nthToken(req.Text, 1)
			idx, err := strconv.Atoi(arg)
			if arg == "" || err != nil {
				return req.Reply("Usage: /model <n>\n\n" + renderModelList(entries, active))
			}

			name, serr := rt.SetActiveModel(idx)
			if serr != nil {
				return req.Reply(serr.Error())
			}
			return req.Reply(fmt.Sprintf("Model set to %d: %s", idx, name))
		},
	}
}

// renderModelList renders a numbered (from 0) list of the agent's candidate
// models, marking the active entry, with a footer pointing to /model.
func renderModelList(entries []ModelEntry, active int) string {
	var b strings.Builder
	b.WriteString("Configured Models:\n")
	for i, e := range entries {
		marker := "  "
		if i == active {
			marker = "▶ "
		}
		if e.Provider != "" {
			fmt.Fprintf(&b, "%s%d: %s  (%s)\n", marker, i, e.Name, e.Provider)
		} else {
			fmt.Fprintf(&b, "%s%d: %s\n", marker, i, e.Name)
		}
	}
	b.WriteString("\nUse /model <n> to switch.")
	return b.String()
}
