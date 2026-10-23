package devices

import (
	"context"
	"sync"
	"time"

	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/constants"
	"github.com/PivotLLM/ClawEh/devices/events"
	"github.com/PivotLLM/ClawEh/devices/sources"
	"github.com/PivotLLM/ClawEh/logger"
)

type Service struct {
	bus     *bus.MessageBus
	target  TargetFunc
	sources []events.EventSource
	alerter alerter.Alerter
	enabled bool
	ctx     context.Context
	cancel  context.CancelFunc
	mu      sync.RWMutex
}

// TargetFunc returns where device notifications are delivered: the default
// agent's default channel and chat. ok is false when none is configured.
type TargetFunc func() (channel, chatID string, ok bool)

type Config struct {
	Enabled    bool
	MonitorUSB bool // When true, monitor USB hotplug (Linux only)
	// Target resolves the delivery channel at notification time; nil means
	// notifications are not delivered.
	Target TargetFunc
	// Alerter receives operator alerts for sources that fail to start; nil
	// means none.
	Alerter alerter.Alerter
	// Future: MonitorBluetooth, MonitorPCI, etc.
}

func NewService(cfg Config) *Service {
	s := &Service{
		target:  cfg.Target,
		alerter: cfg.Alerter,
		enabled: cfg.Enabled,
		sources: make([]EventSource, 0),
	}
	if s.alerter == nil {
		s.alerter = alerter.Nop{}
	}

	if cfg.Enabled && cfg.MonitorUSB {
		s.sources = append(s.sources, sources.NewUSBMonitor())
	}

	return s
}

func (s *Service) SetBus(msgBus *bus.MessageBus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.bus = msgBus
}

func (s *Service) Start(ctx context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.enabled || len(s.sources) == 0 {
		logger.InfoC("devices", "Device event service disabled or no sources")
		return nil
	}

	svcCtx, cancel := context.WithCancel(ctx)
	s.ctx, s.cancel = svcCtx, cancel

	for _, src := range s.sources {
		eventCh, err := src.Start(svcCtx)
		if err != nil {
			logger.ErrorCF("devices", "Failed to start source", map[string]any{
				"kind":  src.Kind(),
				"error": err.Error(),
			})
			s.alerter.Send(alerter.Alert{
				Title:       "Device source not started",
				Description: string(src.Kind()) + ": device events from this source are unavailable",
				Details:     err.Error(),
				EventID:     "devices:" + string(src.Kind()),
			})
			continue
		}
		go s.handleEvents(svcCtx, src.Kind(), eventCh)
		logger.InfoCF("devices", "Device source started", map[string]any{
			"kind": src.Kind(),
		})
	}

	logger.InfoC("devices", "Device event service started")
	return nil
}

func (s *Service) Stop() {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.cancel != nil {
		s.cancel()
		s.cancel = nil
	}

	for _, src := range s.sources {
		if err := src.Stop(); err != nil {
			logger.WarnCF("devices", "Failed to stop device source", map[string]any{"error": err.Error()})
		}
	}

	logger.InfoC("devices", "Device event service stopped")
}

func (s *Service) handleEvents(ctx context.Context, kind events.Kind, eventCh <-chan *events.DeviceEvent) {
	for ev := range eventCh {
		if ev == nil {
			continue
		}
		s.sendNotification(ctx, ev)
	}
}

func (s *Service) sendNotification(ctx context.Context, ev *events.DeviceEvent) {
	s.mu.RLock()
	msgBus := s.bus
	s.mu.RUnlock()

	if msgBus == nil {
		return
	}

	var platform, userID string
	ok := false
	if s.target != nil {
		platform, userID, ok = s.target()
	}
	if !ok || platform == "" || userID == "" || constants.IsInternalChannel(platform) {
		logger.DebugCF("devices", "No default channel for the default agent, skipping notification", map[string]any{
			"event": ev.FormatMessage(),
		})
		return
	}

	msg := ev.FormatMessage()
	pubCtx, pubCancel := context.WithTimeout(ctx, 5*time.Second)
	defer pubCancel()
	if err := msgBus.PublishOutbound(pubCtx, bus.OutboundMessage{
		Channel: platform,
		ChatID:  userID,
		Content: msg,
	}); err != nil {
		logger.WarnCF("devices", "Failed to publish device notification", map[string]any{
			"kind":   ev.Kind,
			"action": ev.Action,
			"to":     platform,
			"error":  err.Error(),
		})
		return
	}

	logger.InfoCF("devices", "Device notification sent", map[string]any{
		"kind":   ev.Kind,
		"action": ev.Action,
		"to":     platform,
	})
}
