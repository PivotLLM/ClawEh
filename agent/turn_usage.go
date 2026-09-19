// ClawEh
// License: MIT

package agent

import (
	"github.com/PivotLLM/ClawEh/global"
	"github.com/PivotLLM/ClawEh/providers"
)

// addTurnUsage folds one successful LLM call's dispatch status into the turn's
// usage accumulator. No-op when the caller did not ask for usage (u == nil).
// The model the provider reports as served is preferred over the requested one,
// so a turn that failed over records the model that actually answered.
func addTurnUsage(u *global.TurnUsage, resp *providers.LLMResponse, provider, requestedModel string) {
	if u == nil || resp == nil {
		return
	}
	model := requestedModel
	st := resp.Status
	if st == nil {
		u.Add(model, provider, 0, 0, 0, 0, 0)
		return
	}
	if st.Model != "" {
		model = st.Model
	}
	u.Add(model, provider, st.InputTokens, st.OutputTokens, st.CacheReadTokens, st.CacheCreationTokens, st.CostUSD)
}
