package logger

import (
	"io"
	"sync"
	"testing"
)

// TestLogWhileReconfiguring logs from several goroutines while the level and
// the output are changed. Under -race it fails if logMessage reads the shared
// logger state without the lock its writers hold.
func TestLogWhileReconfiguring(t *testing.T) {
	prev := GetLevel()
	defer SetLevel(prev)

	stop := make(chan struct{})
	var wg sync.WaitGroup
	for range 4 {
		wg.Go(func() {
			l := NewLogger("test")
			for {
				select {
				case <-stop:
					return
				default:
					l.Infof("line from alice")
				}
			}
		})
	}

	for i := range 200 {
		restore := RedirectForTest(io.Discard)
		if i%2 == 0 {
			SetLevel(WARN)
		} else {
			SetLevel(DEBUG)
		}
		restore()
	}
	close(stop)
	wg.Wait()
}
