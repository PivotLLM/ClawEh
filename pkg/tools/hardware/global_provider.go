// ClawEh
// License: MIT
//
// Copyright (c) 2026 Tenebris Technologies Inc.

package hardware

import (
	"runtime"

	"github.com/PivotLLM/ClawEh/pkg/global"
	"github.com/PivotLLM/ClawEh/pkg/tools"
)

// GlobalProvider exposes the hardware tools through the transport-neutral global
// layer with BARE names ("i2c", "spi"). The aggregator mounts it under the "hw"
// namespace, so the published names are "hw_i2c" / "hw_spi". It reuses the
// existing I2CTool / SPITool logic and converts the result at the boundary, so
// behaviour is unchanged.
var GlobalProvider globalHardwareProvider

type globalHardwareProvider struct{}

// Namespace/Description/Available satisfy global.HostMeta. Available gates the
// whole package on the host being Linux (the bus tools require Linux device
// nodes); when false the host reports the tools as blocked.
func (globalHardwareProvider) Namespace() string   { return "hw" }
func (globalHardwareProvider) Description() string { return "Hardware I2C/SPI bus interaction" }

func (globalHardwareProvider) Available(cfg any) (bool, string) {
	if runtime.GOOS != "linux" {
		return false, "requires_linux"
	}
	return true, ""
}

func (globalHardwareProvider) RegisterTools(deps global.Deps) []global.ToolDefinition {
	// The tool constructors take no config, so they can be built unconditionally;
	// their static metadata is read from the constructed instances.
	i2c := NewI2CTool()
	spi := NewSPITool()

	return []global.ToolDefinition{
		{
			Name:        "i2c",
			Description: i2c.Description(),
			RawSchema:   i2c.Parameters(),
			Category:    "hardware",
			// DefaultAllow nil ⇒ denied by default (DefaultEnabled:false).
			DefaultAllow: nil,
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(i2c.Execute(call.Ctx, call.Args)), nil
			},
		},
		{
			Name:        "spi",
			Description: spi.Description(),
			RawSchema:   spi.Parameters(),
			Category:    "hardware",
			// DefaultAllow nil ⇒ denied by default (DefaultEnabled:false).
			DefaultAllow: nil,
			Handler: func(call *global.ToolCall) (*global.Result, error) {
				return tools.ResultToGlobal(spi.Execute(call.Ctx, call.Args)), nil
			},
		},
	}
}
