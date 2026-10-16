// ClawEh
// License: MIT

package channels

import (
	"context"
	"slices"
	"sync"

	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/media"
)

// ReloadResult names the channels a Reload kept running, rebuilt, added and
// removed, each sorted.
type ReloadResult struct {
	Kept      []string
	Restarted []string
	Started   []string
	Stopped   []string
}

// detached is a channel taken out of service by a Reload, to be stopped.
type detached struct {
	name    string
	channel Channel
	worker  *channelWorker
	cancel  context.CancelFunc
	// cancelStart cancels the context the channel was started with.
	cancelStart context.CancelFunc
	// removed is set when the configuration no longer asks for the channel
	// (as opposed to a rebuild).
	removed bool
}

// RemovalAware is a channel that keeps state beyond its own life (Matrix
// keeps its sync position for the channel that replaces it) and must forget
// it once the configuration no longer asks for the channel. The manager
// calls Removed after stopping it.
type RemovalAware interface {
	Removed()
}

// Reload applies cfg (and the media store, which a configuration save may
// replace) to the running channels: a channel whose inputs (see channelSpec)
// are unchanged keeps running untouched, one whose inputs changed or cannot
// be compared is stopped and rebuilt, a new one is built and started, and one
// cfg no longer asks for is stopped. Outbound messages to a channel being
// rebuilt are held and delivered once it has started. It returns once every
// rebuilt or new channel has started or failed its first attempt; ctx is
// handed to their Start, like StartAll's.
func (m *Manager) Reload(ctx context.Context, cfg *config.Config, store media.MediaStore) ReloadResult {
	m.reloadMu.Lock()
	defer m.reloadMu.Unlock()

	// Outside m.mu: SecMsg discovery may block on a daemon.
	specs := m.plan(cfg) //nolint:contextcheck // SecMsg account discovery runs on its own context, as when the manager is built

	var (
		res     ReloadResult
		stopped []detached
	)
	build := make([]channelSpec, 0, len(specs))
	m.mu.Lock()
	storeChanged := store != m.mediaStore
	m.mediaStore = store
	running := m.dispatchCtx != nil
	wanted := make(map[string]bool, len(specs))
	for _, s := range specs {
		wanted[s.name] = true
		_, exists := m.channels[s.name]
		switch {
		case exists && !storeChanged && s.inputs != "" && s.inputs == m.inputs[s.name]:
			res.Kept = append(res.Kept, s.name)
			m.specs[s.name] = s
			continue
		case exists:
			res.Restarted = append(res.Restarted, s.name)
			stopped = append(stopped, m.detachLocked(s.name))
		default:
			res.Started = append(res.Started, s.name)
		}
		build = append(build, s)
		if running {
			m.held[s.name] = &heldOutbound{}
		}
	}
	for name := range m.channels {
		if wanted[name] {
			continue
		}
		res.Stopped = append(res.Stopped, name)
		d := m.detachLocked(name)
		d.removed = true
		stopped = append(stopped, d)
		delete(m.channels, name)
		delete(m.inputs, name)
		delete(m.specs, name)
	}
	m.mu.Unlock()

	m.stopDetached(ctx, stopped)

	toStart := make(map[string]Channel, len(build))
	for _, s := range build {
		ch := m.buildChannel(s)
		m.mu.Lock()
		if ch == nil {
			delete(m.channels, s.name)
			delete(m.inputs, s.name)
			delete(m.specs, s.name)
			text, media := m.takeHeldLocked(s.name)
			m.mu.Unlock()
			dropHeld(s.name, text, media, "could not be built")
			continue
		}
		m.channels[s.name] = ch
		m.inputs[s.name] = s.inputs
		m.specs[s.name] = s
		toStart[s.name] = ch
		m.mu.Unlock()
	}
	if running {
		m.startChannels(ctx, toStart)
	}

	for _, names := range [][]string{res.Kept, res.Restarted, res.Started, res.Stopped} {
		slices.Sort(names)
	}
	logger.InfoCF("channels", "Channels reloaded", map[string]any{
		"kept":      res.Kept,
		"restarted": res.Restarted,
		"started":   res.Started,
		"stopped":   res.Stopped,
	})
	return res
}

// detachLocked takes channel name out of service: its worker and context are
// removed from the maps (so nothing new is queued on it) and returned to be
// stopped. A channel without a worker (still retrying its start) has its
// context cancelled here, under m.mu, so a retry that succeeds from now on
// sees it is no longer current. The channel itself stays in m.channels.
// m.mu must be held.
func (m *Manager) detachLocked(name string) detached {
	d := detached{
		name:    name,
		channel: m.channels[name],
		worker:  m.workers[name],
		cancel:  m.cancels[name],

		cancelStart: m.startCancels[name],
	}
	if d.worker == nil && d.cancel != nil {
		d.cancel()
	}
	delete(m.workers, name)
	delete(m.cancels, name)
	delete(m.startCancels, name)
	return d
}

// stopDetached stops the detached channels in parallel: what their workers
// have queued is sent first (until stopAllTimeout, after which it is
// abandoned, see waitWorker), then the channel is stopped, its context
// cancelled, and a removed one told so (RemovalAware).
func (m *Manager) stopDetached(ctx context.Context, ds []detached) {
	if len(ds) == 0 {
		return
	}
	stopCtx, cancelStop := context.WithTimeout(ctx, stopAllTimeout)
	defer cancelStop()

	var wg sync.WaitGroup
	for _, d := range ds {
		wg.Go(func() {
			if d.cancel != nil {
				defer d.cancel()
			}
			if d.cancelStart != nil {
				defer d.cancelStart() // after Stop: it is the channel's running context
			}
			if d.worker != nil {
				d.worker.closeQueues()
				waitWorker(stopCtx, d.name, d.worker, d.cancel)
			}
			err := stopOne(stopCtx, d.name, d.channel)
			if r, ok := d.channel.(RemovalAware); ok && d.removed {
				r.Removed()
			}
			if err != nil {
				return // logged by stopOne; the channel is replaced or gone either way
			}
		})
	}
	wg.Wait()
}
