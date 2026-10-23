package matrix

import (
	"context"
	"sync"
	"time"

	"maunium.net/go/mautrix/id"
)

// seenEventsMax bounds how many handled message event ids an account
// remembers, oldest dropped first.
const seenEventsMax = 1024

// accountState is what the Matrix account keeps across channel rebuilds in
// this process: a configuration save that changes the Matrix settings builds a
// new channel, and without this the new one would either drop what arrived
// while it was being rebuilt or handle it twice.
//
//   - firstStart is when a channel for the account first started, at the
//     millisecond, the precision of a Matrix event's origin_server_ts: an
//     event stamped in the start's own millisecond is handled, one in an
//     earlier millisecond predates the channel and never is.
//   - resumeFrom is the next_batch token of the last sync whose events were all
//     handled. A rebuilt channel syncs from there, so what arrived during the
//     rebuild is fetched, not skipped.
//   - seen holds the ids of message events already handled, so a batch the
//     previous channel was stopped in the middle of is not handled twice.
//
// Only one account is kept: a channel for another account (or one built after
// the channel was turned off or removed, see MatrixChannel.Removed) starts
// fresh.
type accountState struct {
	key        string
	mu         sync.Mutex
	firstStart time.Time
	filterID   string
	resumeFrom string
	seen       map[id.EventID]struct{}
	order      []id.EventID
}

// current is the account state of the process, nil until a channel is built.
var current = struct {
	sync.Mutex
	st *accountState
}{}

// accountKey identifies an account: "<homeserver> <user id>".
func accountKey(homeserver, userID string) string { return homeserver + " " + userID }

// accountStateFor returns the state of the account, fresh unless it is the
// account the previous channel served.
func accountStateFor(homeserver, userID string) *accountState {
	key := accountKey(homeserver, userID)
	current.Lock()
	defer current.Unlock()
	if current.st == nil || current.st.key != key {
		current.st = &accountState{key: key, seen: map[id.EventID]struct{}{}}
	}
	return current.st
}

// forgetAccount drops st, so the next channel starts fresh. A newer state
// (another account's) is left alone.
func forgetAccount(st *accountState) {
	current.Lock()
	defer current.Unlock()
	if current.st == st {
		current.st = nil
	}
}

// started records now, at the millisecond, as the first start unless one is
// recorded, and returns the first start.
func (s *accountState) started(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstStart.IsZero() {
		s.firstStart = now.Truncate(time.Millisecond)
	}
	return s.firstStart
}

// processed records that every event up to nextBatch has been handled.
func (s *accountState) processed(nextBatch string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resumeFrom = nextBatch
}

// claim records eventID and reports whether it had not been handled before.
func (s *accountState) claim(eventID id.EventID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.seen[eventID]; ok {
		return false
	}
	s.seen[eventID] = struct{}{}
	s.order = append(s.order, eventID)
	if len(s.order) > seenEventsMax {
		delete(s.seen, s.order[0])
		s.order = s.order[1:]
	}
	return true
}

// handled reports whether eventID has been claimed.
func (s *accountState) handled(eventID id.EventID) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.seen[eventID]
	return ok
}

// release undoes a claim whose event could not be handed on, so the channel
// that fetches it again handles it.
func (s *accountState) release(eventID id.EventID) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.seen, eventID)
	for i, e := range s.order {
		if e == eventID {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
}

// accountSyncStore is the mautrix.SyncStore of a channel: it resumes from the
// last fully handled sync of the account (see accountState). The token mautrix
// saves before handling a response is ignored, since a channel stopped while
// handling it would make its successor skip the rest of that batch.
type accountSyncStore struct{ st *accountState }

func (s accountSyncStore) SaveFilterID(_ context.Context, _ id.UserID, filterID string) error {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	s.st.filterID = filterID
	return nil
}

func (s accountSyncStore) LoadFilterID(context.Context, id.UserID) (string, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	return s.st.filterID, nil
}

func (accountSyncStore) SaveNextBatch(context.Context, id.UserID, string) error { return nil }

func (s accountSyncStore) LoadNextBatch(context.Context, id.UserID) (string, error) {
	s.st.mu.Lock()
	defer s.st.mu.Unlock()
	return s.st.resumeFrom, nil
}
