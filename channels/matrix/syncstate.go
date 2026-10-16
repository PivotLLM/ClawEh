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

// accountState is what one Matrix account keeps across channel rebuilds in
// this process: a configuration save that changes the Matrix settings builds a
// new channel, and without this the new one would either drop what arrived
// while it was being rebuilt or handle it twice.
//
//   - firstStart is when a channel for the account first started. Events older
//     than that predate ClawEh and are never handled.
//   - resumeFrom is the next_batch token of the last sync whose events were all
//     handled. A rebuilt channel syncs from there, so what arrived during the
//     rebuild is fetched, not skipped.
//   - seen holds the ids of message events already handled, so a batch the
//     previous channel was stopped in the middle of is not handled twice.
type accountState struct {
	mu         sync.Mutex
	firstStart time.Time
	filterID   string
	resumeFrom string
	seen       map[id.EventID]struct{}
	order      []id.EventID
}

// accounts maps "<homeserver> <user id>" to its state for the process.
var accounts = struct {
	sync.Mutex
	m map[string]*accountState
}{m: map[string]*accountState{}}

// accountStateFor returns the process-wide state of the account.
func accountStateFor(homeserver, userID string) *accountState {
	key := homeserver + " " + userID
	accounts.Lock()
	defer accounts.Unlock()
	st, ok := accounts.m[key]
	if !ok {
		st = &accountState{seen: map[id.EventID]struct{}{}}
		accounts.m[key] = st
	}
	return st
}

// started records now as the first start unless one is recorded, and returns
// the first start.
func (s *accountState) started(now time.Time) time.Time {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.firstStart.IsZero() {
		s.firstStart = now
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
