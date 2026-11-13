// ClawEh
// License: MIT

package gateway

import (
	"context"
	"time"

	"github.com/PivotLLM/ClawEh/agent"
	"github.com/PivotLLM/ClawEh/classifier"
	"github.com/PivotLLM/ClawEh/logger"
	toolsclassifier "github.com/PivotLLM/ClawEh/tools/classifier"
)

// classifierCloseTimeout bounds how long shutdown waits for the classifier
// runs in progress to stop.
const classifierCloseTimeout = 10 * time.Second

// newClassifierService builds the process's classifier service over host and
// installs it for the classifier tools. Called before the agent loop exists,
// because the loop builds every agent's tools as it starts; host is bound to
// the loop afterwards. The install's maximums (classifiers.limits) and the
// agents whose switch is on are read from the loop's current configuration
// whenever the service needs them.
func newClassifierService(host *agent.ClassifierHost) *classifier.Service {
	svc := classifier.New(classifier.Host{
		Tools:        host,
		Model:        host,
		Instructions: host,
		Notifier:     host,
		Auditor:      host,
		Logger:       logger.NewLogger("classifier"),
		Modes:        host.Modes,
		Ceilings:     host.Ceilings,
		Scopes:       host.Scopes,
		Redact:       host.Redact,
	})
	toolsclassifier.SetService(svc)
	return svc
}

// startClassifiers starts the classifiers' scheduler once the agent loop
// runs (started is closed), so their tools and models are there. It returns
// at once; done (when set) is called once the scheduler has started or the
// start was abandoned (ctx ended first).
func startClassifiers(ctx context.Context, svc *classifier.Service, started <-chan struct{}, done func()) {
	go func() {
		if done != nil {
			defer done()
		}
		select {
		case <-started:
		case <-ctx.Done():
			return
		}
		svc.Start(ctx)
		logger.InfoC("classifier", "Classifiers started")
	}()
}

// closeClassifiers stops the scheduler and every classifier run (their calls
// are cancelled; an item not finished is taken again at the next start) and
// closes their state. Called before the agent loop stops.
func closeClassifiers(svc *classifier.Service) {
	if svc == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), classifierCloseTimeout)
	defer cancel()
	if err := svc.Close(ctx); err != nil {
		logger.WarnCF("classifier", "Classifiers did not all stop in time", map[string]any{"error": err.Error()})
	}
}
