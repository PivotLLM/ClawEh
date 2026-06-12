package msg

import (
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the msg tools through the transport-neutral global
// layer with BARE names ("send", "send_file"). The aggregator mounts it under
// the "msg" namespace, so the published names are "msg_send" / "msg_send_file".
// It reuses the existing MessageTool / SendFileTool logic and converts the
// result at the boundary, so behaviour is unchanged.
var GlobalProvider globalMsgProvider

type globalMsgProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta.
func (globalMsgProvider) Namespace() string   { return "msg" }
func (globalMsgProvider) Description() string { return "Message sending to users and channels" }

func (globalMsgProvider) Available(cfg any) (bool, string) { return true, "" }

func (globalMsgProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// Recover the real config and Claw host deps. Enumeration (Describe) passes a
	// zero Deps; handlers are never called then, so nil recovery is safe.
	c, _ := deps.Cfg.(*config.Config)
	cd, _ := deps.Host.(tools.ToolDeps)

	// Metadata is derived from zero-value instances; neither Description() nor
	// Parameters() dereference any fields, so this is safe.
	sendDesc := (&MessageTool{}).Description()
	sendSchema := (&MessageTool{}).Parameters()
	sendFileDesc := (&SendFileTool{}).Description()
	sendFileSchema := (&SendFileTool{}).Parameters()

	// msg_send: shared pre-built instance passed via deps.Host (may be nil if not
	// yet wired). Its concrete type is *MessageTool, but it is handed to us as a
	// tools.Tool, so we delegate through the Tool interface.

	// msg_send_file: construct the real instance only when real config is present.
	var sendFile *SendFileTool
	if c != nil {
		sendFile = NewSendFileTool(
			cd.Workspace,
			c.Agents.Defaults.RestrictToWorkspace,
			c.Agents.Defaults.GetMaxMediaSize(),
			nil, // MediaStore injected later elsewhere
		)
	}

	return []global.ToolDefinition{
		{
			Name:         "send",
			Description:  sendDesc,
			RawSchema:    sendSchema,
			Category:     "communication",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if cd.MessageTool == nil {
					return &global.Result{IsError: true, ForLLM: "message tool not available"}, nil
				}
				return tools.ResultToGlobal(cd.MessageTool.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:         "send_file",
			Description:  sendFileDesc,
			RawSchema:    sendFileSchema,
			Category:     "communication",
			DefaultAllow: global.Allow(true),
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				if sendFile == nil {
					return &global.Result{IsError: true, ForLLM: "send_file tool not available"}, nil
				}
				return tools.ResultToGlobal(sendFile.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}
