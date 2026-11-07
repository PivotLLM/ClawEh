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

// startClassifiers starts the scheduler only once the agent loop runs: the
// scheduler's first look at the scopes finds the loop started. When its
// context ends first, the scheduler never starts: closing the service (which
// waits for the scheduler) finds the scopes never read.
func TestStartClassifiers_WaitsForStarted(t *testing.T) {
	h := gwClassifierHost{}
	var listed atomic.Int32
	newSvc := func(scopes func() []classifier.Scope) *classifier.Service {
		return classifier.New(classifier.Host{Tools: h, Model: h, Logger: gwForumHost{}, Scopes: scopes})
	}

	started := make(chan struct{})
	firstLook := make(chan struct{}, 1)
	svc := newSvc(func() []classifier.Scope {
		select {
		case <-started:
		default:
			t.Error("the scheduler read the scopes before the agent loop started")
		}
		select {
		case firstLook <- struct{}{}:
		default:
		}
		return nil
	})
	done := make(chan struct{})
	startClassifiers(context.Background(), svc, started, func() { close(done) })
	close(started)
	for _, wait := range []struct {
		ch   <-chan struct{}
		what string
	}{{done, "the start"}, {firstLook, "the scheduler's first look at the scopes"}} {
		select {
		case <-wait.ch:
		case <-time.After(5 * time.Second):
			t.Fatalf("timed out waiting for %s", wait.what)
		}
	}
	closeClassifiers(svc)

	abandoned := newSvc(func() []classifier.Scope {
		listed.Add(1)
		return nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	gaveUp := make(chan struct{})
	startClassifiers(ctx, abandoned, make(chan struct{}), func() { close(gaveUp) })
	cancel()
	select {
	case <-gaveUp:
	case <-time.After(5 * time.Second):
		t.Fatal("the start did not give up when its context ended")
	}
	closeClassifiers(abandoned)
	if listed.Load() != 0 {
		t.Fatal("the scheduler started although the context ended first")
	}
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
