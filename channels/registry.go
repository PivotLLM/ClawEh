package channels

import (
	"context"
	"slices"
	"sync"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/constants"
)

// ChannelFactory is a constructor function that creates a Channel from config and message bus.
// Each channel subpackage registers one or more factories via init().
type ChannelFactory func(cfg *config.Config, bus *bus.MessageBus) (Channel, error)

// TelegramBotFactory is a constructor function that creates a TelegramChannel from a
// TelegramBotConfig. The channel name is derived from botCfg.ChannelName(). Registered
// by the telegram subpackage to allow the manager to initialize bots without a direct
// import (avoiding cycles).
type TelegramBotFactory func(botCfg config.TelegramBotConfig, bus *bus.MessageBus) (Channel, error)

var (
	factoriesMu sync.RWMutex
	factories   = map[string]ChannelFactory{}

	telegramBotFactoryMu sync.RWMutex
	telegramBotFactory   TelegramBotFactory
)

// SecMsgFactory is a constructor function that creates a channel for one account
// on a secmsg daemon. The channel name is derived from account.ChannelName(daemon).
// Registered by the secmsg subpackage to allow the manager to initialize daemon
// accounts without a direct import (avoiding cycles).
type SecMsgFactory func(daemon config.SecMsgConfig, account config.SecMsgAccountConfig, bus *bus.MessageBus) (Channel, error)

// SecMsgDiscovery queries a daemon at address and returns its linked account ids.
// Registered by the secmsg subpackage alongside the factory so the manager can
// enumerate accounts without a direct import (avoiding cycles).
type SecMsgDiscovery func(ctx context.Context, address string) ([]string, error)

var (
	secmsgFactoryMu sync.RWMutex
	secmsgFactory   SecMsgFactory

	secmsgDiscoveryMu sync.RWMutex
	secmsgDiscovery   SecMsgDiscovery
)

// RegisterSecMsgFactory registers the factory used to create secmsg channels.
// Called from the secmsg subpackage init() to avoid a direct import cycle.
func RegisterSecMsgFactory(f SecMsgFactory) {
	secmsgFactoryMu.Lock()
	defer secmsgFactoryMu.Unlock()
	secmsgFactory = f
}

// getSecMsgFactory returns the registered SecMsgFactory, if any.
func getSecMsgFactory() (SecMsgFactory, bool) {
	secmsgFactoryMu.RLock()
	defer secmsgFactoryMu.RUnlock()
	return secmsgFactory, secmsgFactory != nil
}

// RegisterSecMsgDiscovery registers the daemon account-discovery function.
// Called from the secmsg subpackage init() to avoid a direct import cycle.
func RegisterSecMsgDiscovery(f SecMsgDiscovery) {
	secmsgDiscoveryMu.Lock()
	defer secmsgDiscoveryMu.Unlock()
	secmsgDiscovery = f
}

// getSecMsgDiscovery returns the registered SecMsgDiscovery, if any.
func getSecMsgDiscovery() (SecMsgDiscovery, bool) {
	secmsgDiscoveryMu.RLock()
	defer secmsgDiscoveryMu.RUnlock()
	return secmsgDiscovery, secmsgDiscovery != nil
}

// RegisterFactory registers a named channel factory. Called from subpackage init() functions.
func RegisterFactory(name string, f ChannelFactory) {
	factoriesMu.Lock()
	defer factoriesMu.Unlock()
	factories[name] = f
}

// getFactory looks up a channel factory by name.
func getFactory(name string) (ChannelFactory, bool) {
	factoriesMu.RLock()
	defer factoriesMu.RUnlock()
	f, ok := factories[name]
	return f, ok
}

// RegisterTelegramBotFactory registers the factory used to create named Telegram bots.
// Called from the telegram subpackage init() to avoid a direct import cycle.
func RegisterTelegramBotFactory(f TelegramBotFactory) {
	telegramBotFactoryMu.Lock()
	defer telegramBotFactoryMu.Unlock()
	telegramBotFactory = f
}

// getTelegramBotFactory returns the registered TelegramBotFactory, if any.
func getTelegramBotFactory() (TelegramBotFactory, bool) {
	telegramBotFactoryMu.RLock()
	defer telegramBotFactoryMu.RUnlock()
	return telegramBotFactory, telegramBotFactory != nil
}

var (
	builtinsMu sync.RWMutex
	builtins   = map[string]ChannelFactory{}
)

// RegisterBuiltin registers a channel that every channel manager builds, on
// every (re)build, regardless of the configuration: it is started with
// workers and receives outbound messages like a configured channel. The
// factory may return a nil channel to skip it for a given configuration. A
// configured channel of the same name wins. Called from init() by the package
// that provides the channel.
//
// It panics on an empty name, a nil factory, or an internal channel name
// (outbound messages to those are dropped, so the channel could never reply).
func RegisterBuiltin(name string, f ChannelFactory) {
	if name == "" || f == nil || constants.IsInternalChannel(name) {
		panic("channels: invalid built-in channel registration: " + name)
	}
	builtinsMu.Lock()
	defer builtinsMu.Unlock()
	builtins[name] = f
}

// builtinNames returns the registered built-in channel names, sorted so the
// build order is stable.
func builtinNames() []string {
	builtinsMu.RLock()
	defer builtinsMu.RUnlock()
	names := make([]string, 0, len(builtins))
	for name := range builtins {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// getBuiltin looks up a built-in channel factory by name.
func getBuiltin(name string) (ChannelFactory, bool) {
	builtinsMu.RLock()
	defer builtinsMu.RUnlock()
	f, ok := builtins[name]
	return f, ok
}
