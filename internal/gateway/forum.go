// ClawEh
// License: MIT

package gateway

import (
	"context"
	"time"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/forum"
	"github.com/PivotLLM/ClawEh/logger"
	toolsforum "github.com/PivotLLM/ClawEh/tools/forum"
)

// forumCloseTimeout bounds how long shutdown waits for the forums' runs and
// completion notices to stop.
const forumCloseTimeout = 10 * time.Second

// newForumService builds the process's forum service over host and installs
// it for the forum tools. Called before the agent loop exists, because the
// loop builds every agent's tools as it starts; host is bound to the loop
// afterwards.
func newForumService(host *agent.ForumHost) *forum.Service {
	svc := forum.New(forum.Host{
		Messenger: host,
		Agents:    host,
		Notifier:  host,
		Logger:    logger.NewLogger("forum"),
		Schemas:   forum.JSONSchemaValidator{},
		OnStuck:   host.OnStuck,
		Cooldown:  host.Cooldown,
	})
	toolsforum.SetService(svc)
	return svc
}

// recoverForums resumes the forums of every configured agent, whatever its
// `forum` switch (the switch gates the tools only), once the agent loop
// accepts asks (started is closed). It returns at once; recovery runs in
// the background, and done (when set) is called when it has finished or
// was abandoned.
func recoverForums(ctx context.Context, svc *forum.Service, scopes func() []forum.Scope, started <-chan struct{}, done func()) {
	go func() {
		if done != nil {
			defer done()
		}
		select {
		case <-started:
		case <-ctx.Done():
			return
		}
		scopes := scopes()
		if len(scopes) == 0 {
			return
		}
		if err := svc.Recover(ctx, scopes); err != nil {
			logger.WarnCF("forum", "Forum recovery finished with errors",
				map[string]any{"agents": len(scopes), "error": err.Error()})
			return
		}
		logger.InfoCF("forum", "Forum recovery finished", map[string]any{"agents": len(scopes)})
	}()
}

// closeForums stops every forum run without changing its state on disk, so
// it resumes at the next start. Called before the agent loop stops.
func closeForums(svc *forum.Service) {
	if svc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), forumCloseTimeout)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		logger.WarnCF("forum", "Forums did not all stop in time", map[string]any{"error": err.Error()})
	}
}
