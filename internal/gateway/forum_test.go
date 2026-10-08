// ClawEh
// License: MIT

package gateway

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/PivotLLM/ClawEh/forum"
)

// recoverForums waits for the agent loop to accept asks before it lists the
// scopes and recovers, and gives up when its context ends first.
func TestRecoverForums_WaitsForStarted(t *testing.T) {
	h := gwForumHost{}
	svc := forum.New(forum.Host{Messenger: h, Agents: h, Notifier: h, Logger: h})
	t.Cleanup(func() {
		if err := svc.Close(context.Background()); err != nil {
			t.Errorf("Close: %v", err)
		}
	})
	base := t.TempDir()

	var listed atomic.Int32
	scopes := func() []forum.Scope {
		listed.Add(1)
		return []forum.Scope{{AgentID: "alice", BaseDirectory: base}}
	}
	started, done := make(chan struct{}), make(chan struct{})
	recoverForums(context.Background(), svc, scopes, started, func() { close(done) })
	time.Sleep(50 * time.Millisecond)
	if listed.Load() != 0 {
		t.Fatal("recovery ran before the agent loop started")
	}
	close(started)
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not finish after the loop started")
	}
	if listed.Load() != 1 {
		t.Fatalf("scopes listed %d times, want once", listed.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	abandoned := make(chan struct{})
	recoverForums(ctx, svc, scopes, make(chan struct{}), func() { close(abandoned) })
	cancel()
	select {
	case <-abandoned:
	case <-time.After(5 * time.Second):
		t.Fatal("recovery did not give up when its context ended")
	}
	if listed.Load() != 1 {
		t.Fatal("an abandoned recovery listed the scopes")
	}
}

// gwForumHost satisfies the forum host interfaces for a service that runs
// nothing.
type gwForumHost struct{}

func (gwForumHost) Ask(context.Context, string, string, time.Duration) (forum.Reply, error) {
	return forum.Reply{}, errors.New("unused")
}
func (gwForumHost) Exists(context.Context, string) (bool, error)            { return false, nil }
func (gwForumHost) MayTarget(context.Context, string, string) (bool, error) { return false, nil }
func (gwForumHost) Models(context.Context, string) ([]forum.ModelInfo, error) {
	return nil, nil
}

func (gwForumHost) CreateClone(context.Context, forum.CloneSpec) (string, error) {
	return "", errors.New("unused")
}

func (gwForumHost) CreateFresh(context.Context, forum.FreshSpec) (string, error) {
	return "", errors.New("unused")
}
func (gwForumHost) Delete(context.Context, string, string) error { return nil }
func (gwForumHost) Touch(context.Context, string, string) error  { return nil }
func (gwForumHost) ForumFinished(context.Context, forum.Origin, forum.Chat, *forum.Result) error {
	return nil
}
func (gwForumHost) Debugf(string, ...any) {}
func (gwForumHost) Infof(string, ...any)  {}
func (gwForumHost) Warnf(string, ...any)  {}
func (gwForumHost) Errorf(string, ...any) {}
