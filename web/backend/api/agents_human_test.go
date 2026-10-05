// ClawEh
// License: MIT

package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/PivotLLM/ClawEh/config"
)

// GET /api/agents/human names each agent breaking the human-agent rules, so
// the Agents page can say why it is not running; a valid setup lists none.
func TestHandleHumanAgentProblems(t *testing.T) {
	for _, tc := range []struct {
		name    string
		binding bool
		want    int
	}{
		{"with its chat", true, 0},
		{"without a default chat", false, 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			configPath := setupTestEnv(t)
			cfg, err := config.LoadConfig(configPath)
			if err != nil {
				t.Fatal(err)
			}
			cfg.Providers = append(cfg.Providers, config.Provider{Name: "People", Protocol: config.HumanProtocol})
			cfg.Models = append(cfg.Models, config.ModelConfig{ModelName: "Bob (human)", Model: "bob", Provider: "People", Enabled: true})
			cfg.Agents.List = append(cfg.Agents.List, config.AgentConfig{ID: "bob", Models: []string{"Bob (human)"}})
			if tc.binding {
				cfg.Bindings = append(cfg.Bindings, config.AgentBinding{
					AgentID: "bob", Default: true,
					Match: config.BindingMatch{Channel: "telegram-main", Peer: &config.PeerMatch{Kind: "direct", ID: "4242"}},
				})
			}
			if err := config.SaveConfig(configPath, cfg); err != nil {
				t.Fatal(err)
			}

			h := NewHandler(configPath)
			mux := http.NewServeMux()
			h.RegisterRoutes(mux)
			rec := httptest.NewRecorder()
			mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/agents/human", nil))
			if rec.Code != http.StatusOK {
				t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
			}
			var body struct {
				Problems []humanAgentProblem `json:"problems"`
				Humans   []string            `json:"human_agents"`
			}
			if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
				t.Fatal(err)
			}
			if len(body.Humans) != 1 || body.Humans[0] != "bob" {
				t.Errorf("human_agents = %v, want [bob]", body.Humans)
			}
			if len(body.Problems) != tc.want {
				t.Fatalf("problems = %+v, want %d", body.Problems, tc.want)
			}
			if tc.want > 0 && (body.Problems[0].Agent != "bob" || !strings.Contains(body.Problems[0].Message, "needs a default chat") || body.Problems[0].Kind != "not_running" || body.Problems[0].Link != "/channels") {
				t.Errorf("problem = %+v", body.Problems[0])
			}
		})
	}
}
