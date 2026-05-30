//go:build !linux

package hardware

import "github.com/PivotLLM/ClawEh/pkg/tools"

// transfer is a stub for non-Linux platforms.
func (t *SPITool) transfer(args map[string]any) *tools.ToolResult {
	return tools.ErrorResult("SPI is only supported on Linux")
}

// readDevice is a stub for non-Linux platforms.
func (t *SPITool) readDevice(args map[string]any) *tools.ToolResult {
	return tools.ErrorResult("SPI is only supported on Linux")
}
