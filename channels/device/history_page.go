package device

import (
	"errors"
	"unicode/utf8"
)

// chat.history paging, as the OpenClaw Gateway protocol defines it: the window
// is counted from the newest message. offset skips that many of the most recent
// messages, limit then takes the page before them, and maxChars cuts each
// message's text. A client pages back by sending nextOffset as its next offset.
const (
	historyDefaultLimit = 200
	historyMaxLimit     = 1000
	historyMaxChars     = 500_000
)

// historyPageParams are the optional chat.history paging fields. Pointers tell
// an absent field from an explicit zero, which is refused.
type historyPageParams struct {
	Limit    *int `json:"limit"`
	Offset   *int `json:"offset"`
	MaxChars *int `json:"maxChars"`
}

var errBadHistoryParams = errors.New("invalid chat.history params")

// historyPage is one page of a transcript plus the facts a client needs to ask
// for the page before it.
type historyPage struct {
	Messages      []DeviceHistoryMessage
	Offset        int
	TotalMessages int
	HasMore       bool
	NextOffset    int // meaningful only when HasMore
}

// pageHistory applies the paging fields to a transcript (oldest first). It
// refuses a limit outside 1..1000, a negative offset, or a maxChars outside
// 1..500000. The returned messages share no storage with history.
func pageHistory(history []DeviceHistoryMessage, p historyPageParams) (historyPage, error) {
	limit := historyDefaultLimit
	if p.Limit != nil {
		if *p.Limit < 1 || *p.Limit > historyMaxLimit {
			return historyPage{}, errBadHistoryParams
		}
		limit = *p.Limit
	}
	offset := 0
	if p.Offset != nil {
		if *p.Offset < 0 {
			return historyPage{}, errBadHistoryParams
		}
		offset = *p.Offset
	}
	maxChars := 0
	if p.MaxChars != nil {
		if *p.MaxChars < 1 || *p.MaxChars > historyMaxChars {
			return historyPage{}, errBadHistoryParams
		}
		maxChars = *p.MaxChars
	}

	total := len(history)
	offset = min(offset, total)
	end := total - offset
	start := max(0, end-limit)

	page := make([]DeviceHistoryMessage, 0, end-start)
	for _, m := range history[start:end] {
		if maxChars > 0 {
			m.Content = truncateChars(m.Content, maxChars)
		}
		page = append(page, m)
	}
	out := historyPage{Messages: page, Offset: offset, TotalMessages: total, HasMore: start > 0}
	if out.HasMore {
		out.NextOffset = offset + len(page)
	}
	return out, nil
}

// truncateChars cuts s to at most maxChars characters, ending in "..." when it
// was cut and there is room for the marker, as the OpenClaw Gateway does.
func truncateChars(s string, maxChars int) string {
	if utf8.RuneCountInString(s) <= maxChars {
		return s
	}
	keep := maxChars
	marker := ""
	if maxChars > 3 {
		keep = maxChars - 3
		marker = "..."
	}
	i := 0
	for n := 0; n < keep; n++ {
		_, size := utf8.DecodeRuneInString(s[i:])
		i += size
	}
	return s[:i] + marker
}
