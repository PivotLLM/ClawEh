//go:build !linux

package hardware

import "github.com/PivotLLM/ClawEh/pkg/tools"

// scan is a stub for non-Linux platforms.
func (t *I2CTool) scan(args map[string]any) *tools.ToolResult {
	return tools.ErrorResult("I2C is only supported on Linux")
}

// readDevice is a stub for non-Linux platforms.
func (t *I2CTool) readDevice(args map[string]any) *tools.ToolResult {
	return tools.ErrorResult("I2C is only supported on Linux")
}

// writeDevice is a stub for non-Linux platforms.
func (t *I2CTool) writeDevice(args map[string]any) *tools.ToolResult {
	return tools.ErrorResult("I2C is only supported on Linux")
}
