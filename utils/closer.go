package utils

import (
	"io"

	"github.com/PivotLLM/ClawEh/logger"
)

// CloseQuietly closes c and logs at debug level if Close reports an error. It
// is for handles whose close error carries nothing the caller can act on:
// files opened for reading, HTTP response bodies, row iterators, listeners
// and stores being torn down. A handle that was written to must have its
// Close error checked by the caller instead, because that is where a flush
// failure surfaces.
func CloseQuietly(c io.Closer) {
	if err := c.Close(); err != nil {
		logger.DebugF("close failed", map[string]any{"error": err.Error()})
	}
}
