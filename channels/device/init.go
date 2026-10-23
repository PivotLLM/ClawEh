package device

import (
	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/internal/tlscert"
)

func init() {
	channels.RegisterFactory("device", func(cfg *config.Config, b *bus.MessageBus) (channels.Channel, error) {
		// The gateway's TLS certificate names are Hosts clients reach this box
		// by, so the device listener answers to them too (nil when the HTTPS
		// listener is off).
		exempt, err := config.CompileLockoutExempt(cfg.Gateway.LockoutExempt)
		if err != nil {
			return nil, err
		}
		trusted, err := config.CompileTrustedProxies(cfg.Gateway.TrustedProxies)
		if err != nil {
			return nil, err
		}
		dc, err := NewDeviceChannel(cfg.Channels.Device, cfg.DataDir(), cfg.Logging.LogMessageContent, b, cfg.Gateway.ExternalURL, tlscert.NamesForConfig(cfg))
		if err != nil {
			return nil, err
		}
		// Rebuilt by a config reload that changes any of factoryInputs, so a
		// changed list applies then.
		dc.server.SetLockoutExempt(exempt)
		dc.SetTrustedProxies(trusted)
		return dc, nil
	})
	channels.RegisterFactoryInputs("device", factoryInputs)
}

// factoryInputs is everything the device factory above reads. Keep the two in
// step: a value read there but missing here is not applied by a reload.
func factoryInputs(cfg *config.Config) any {
	return struct {
		Device            config.DeviceChannelConfig
		DataDir           string
		LogMessageContent bool
		ExternalURL       string
		LockoutExempt     []string
		TrustedProxies    []string
		CertificateNames  []string
	}{
		Device:            cfg.Channels.Device,
		DataDir:           cfg.DataDir(),
		LogMessageContent: cfg.Logging.LogMessageContent,
		ExternalURL:       cfg.Gateway.ExternalURL,
		LockoutExempt:     cfg.Gateway.LockoutExempt,
		TrustedProxies:    cfg.Gateway.TrustedProxies,
		CertificateNames:  tlscert.NamesForConfig(cfg),
	}
}
