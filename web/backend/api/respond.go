// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"

	"github.com/PivotLLM/ClawEh/logger"
)

// encodeJSON writes v as the response body. A failure here means the client
// went away or the value is unencodable; the status is already sent, so all
// that is left to do is record it.
func encodeJSON(w http.ResponseWriter, v any) {
	if err := json.NewEncoder(w).Encode(v); err != nil {
		logger.WarnCF("api", "response write failed", map[string]any{"error": err.Error()})
	}
}
