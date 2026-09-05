package msg

import "testing"

// Tests for send_file.SetMediaStore.
func TestSendFileTool_SetMediaStore(t *testing.T) {
	tool := NewSendFileTool(t.TempDir(), false, 0, nil)
	tool.SetMediaStore(nil)
	// SetMediaStore(nil) should not panic.
}
