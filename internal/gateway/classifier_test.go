// ClawEh
// License: MIT

package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/classifier"
)

// startClassifiers starts the scheduler only once the agent loop runs, and
// not at all when its context ends first; closeClassifiers stops it.
func TestStartClassifiers_WaitsForStarted(t *testing.T) {
	var listed atomic.Int32
	h := gwClassifierHost{}
	newSvc := func() *classifier.Service {
		return classifier.New(classifier.Host{Tools: h, Model: h, Logger: gwForumHost{}, Scopes: func() []classifier.Scope {
			listed.Add(1)
			return nil
		}})
	}

	svc := newSvc()
	started := make(chan struct{})
	startClassifiers(context.Background(), svc, started)
	time.Sleep(50 * time.Millisecond)
	if listed.Load() != 0 {
		t.Fatal("the classifiers started before the agent loop")
	}
	close(started)
	deadline := time.Now().Add(5 * time.Second)
	for listed.Load() == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the classifiers did not start after the loop started")
		}
		time.Sleep(10 * time.Millisecond)
	}
	closeClassifiers(svc)

	listed.Store(0)
	abandoned := newSvc()
	ctx, cancel := context.WithCancel(context.Background())
	startClassifiers(ctx, abandoned, make(chan struct{}))
	cancel()
	time.Sleep(50 * time.Millisecond)
	if listed.Load() != 0 {
		t.Fatal("the classifiers started although the context ended first")
	}
	closeClassifiers(abandoned)
	closeClassifiers(nil)
}

// gwClassifierHost satisfies the classifier host interfaces for a service
// that runs nothing.
type gwClassifierHost struct{}

func (gwClassifierHost) Has(string, string) error { return errors.New("unused") }
func (gwClassifierHost) Call(context.Context, string, string, map[string]any) (string, error) {
	return "", errors.New("unused")
}

func (gwClassifierHost) Complete(context.Context, string, string, string) (string, string, error) {
	return "", "", errors.New("unused")
}
