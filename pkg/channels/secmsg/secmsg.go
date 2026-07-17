// Package secmsg implements a ClawEh channel backed by a secure-messaging
// daemon speaking the secmsg JSON-RPC protocol (e.g. sigd for Signal). The
// daemon is service-agnostic: it advertises the concrete service, capabilities,
// and account addressing in its handshake, and ClawEh learns those at connect
// time. Each enabled SecMsgConfig entry becomes one channel bound to one
// account on one daemon; several daemons/accounts run as separate entries.
package secmsg

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/identity"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/media"

	smclient "github.com/tenebris-tech/secmsg/client"
	"github.com/tenebris-tech/secmsg/schema"
)

// groupChatPrefix marks an outbound ChatID as a group target. The bus carries
// only ChatID on the reply path, but the secmsg protocol needs separate send
// methods for 1:1 vs group; encoding the peer kind into the ChatID we emit on
// inbound lets Send route without a side lookup. Direct IDs are service
// identifiers (UUID/phone) that never carry this prefix.
const groupChatPrefix = "group:"

// maxMessageLen splits long agent replies into daemon-friendly chunks. Signal's
// practical single-message ceiling is ~2000 characters before the client turns
// text into a long-message attachment; staying under it keeps replies inline.
const maxMessageLen = 2000

// dialTimeout bounds a single connect/handshake attempt.
const dialTimeout = 30 * time.Second

// discoveryTimeout bounds the one-shot dial+StatusAll used to enumerate a
// daemon's accounts at channel-manager init. Kept short so a daemon that is down
// at startup does not stall boot; on failure the manager logs and skips the
// daemon, picking its accounts up on the next reload.
const discoveryTimeout = 5 * time.Second

// DiscoverAccounts dials the daemon at address, reads its account status, and
// returns the ids of every linked account. Used by the channel manager to bind
// one channel per account when no accounts are pinned in config.
func DiscoverAccounts(ctx context.Context, address string) ([]string, error) {
	if address == "" {
		return nil, fmt.Errorf("secmsg: address is required")
	}
	ctx, cancel := context.WithTimeout(ctx, discoveryTimeout)
	defer cancel()

	cl, err := smclient.Dial(address, smclient.WithTimeout(discoveryTimeout))
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer cl.Close()

	all, err := cl.StatusAll(ctx)
	if err != nil {
		return nil, fmt.Errorf("status: %w", err)
	}
	var linked []string
	for _, s := range all.Accounts {
		if s.Linked {
			linked = append(linked, s.Account)
		}
	}
	return linked, nil
}

// SecMsgChannel is one connection to one account on one secmsg daemon. A daemon
// hosting several accounts yields one channel (and one connection) per account.
type SecMsgChannel struct {
	*channels.BaseChannel
	addr string
	// wantAccount is the configured account id to bind, or "" to auto-select the
	// daemon's sole linked account.
	wantAccount string

	// mu guards the live client and the service/account addressing resolved from
	// the daemon handshake; the run loop rebuilds them on every reconnect.
	mu      sync.RWMutex
	client  *smclient.Client
	service string
	account string

	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
}

// NewFromConfig builds a secmsg channel for one account on a daemon. It does not
// dial; the connection is established (and retried) by Start.
func NewFromConfig(daemon config.SecMsgConfig, account config.SecMsgAccountConfig, b *bus.MessageBus) (channels.Channel, error) {
	if daemon.Address == "" {
		return nil, fmt.Errorf("secmsg: address is required")
	}
	base := channels.NewBaseChannel(
		account.ChannelName(daemon),
		account,
		b,
		account.AllowFrom,
		channels.WithMaxMessageLength(maxMessageLen),
		channels.WithGroupTrigger(account.GroupTrigger),
	)
	c := &SecMsgChannel{BaseChannel: base, addr: daemon.Address, wantAccount: account.Account}
	base.SetOwner(c)
	return c, nil
}

func (c *SecMsgChannel) Start(ctx context.Context) error {
	c.ctx, c.cancel = context.WithCancel(ctx)
	c.SetRunning(true)
	c.wg.Add(1)
	go c.run()
	return nil
}

func (c *SecMsgChannel) Stop(_ context.Context) error {
	c.SetRunning(false)
	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
	c.mu.Lock()
	if c.client != nil {
		c.client.Close()
		c.client = nil
	}
	c.mu.Unlock()
	return nil
}

// run supervises the daemon connection, reconnecting with capped backoff. A
// closed subscription (daemon restart, dropped socket) surfaces as an error and
// triggers a redial rather than a silent stall.
func (c *SecMsgChannel) run() {
	defer c.wg.Done()
	backoff := time.Second
	const maxBackoff = 30 * time.Second
	for {
		if c.ctx.Err() != nil {
			return
		}
		connected, err := c.connectAndConsume()
		if c.ctx.Err() != nil {
			return
		}
		if err != nil {
			logger.WarnCF("secmsg", "Connection lost — retrying", map[string]any{
				"channel": c.Name(),
				"address": c.addr,
				"backoff": backoff.String(),
				"error":   err.Error(),
			})
		}
		// A session that was established and later dropped reconnects fast; only a
		// daemon that never accepts a connection escalates the backoff.
		if connected {
			backoff = time.Second
		}
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
		}
		if backoff *= 2; backoff > maxBackoff {
			backoff = maxBackoff
		}
	}
}

// connectAndConsume dials, resolves the account, subscribes, and pumps inbound
// notifications until the context is cancelled or the subscription closes.
func (c *SecMsgChannel) connectAndConsume() (connected bool, err error) {
	cl, err := smclient.Dial(c.addr, smclient.WithTimeout(dialTimeout))
	if err != nil {
		return false, fmt.Errorf("dial: %w", err)
	}
	defer cl.Close()

	service := cl.Service()
	account, err := c.resolveAccount(cl, service)
	if err != nil {
		return false, err
	}

	c.mu.Lock()
	c.client, c.service, c.account = cl, service, account
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		if c.client == cl {
			c.client = nil
		}
		c.mu.Unlock()
	}()

	logger.InfoCF("secmsg", "Connected to daemon", map[string]any{
		"channel": c.Name(),
		"service": service,
		"account": account,
		"address": c.addr,
	})

	ch, cancel, err := cl.Subscribe(c.ctx, account)
	if err != nil {
		return false, fmt.Errorf("subscribe: %w", err)
	}
	defer cancel()

	for {
		select {
		case <-c.ctx.Done():
			return true, nil
		case env, ok := <-ch:
			if !ok {
				return true, fmt.Errorf("subscription closed")
			}
			c.handleEnvelope(env)
		}
	}
}

// resolveAccount returns the account to bind. An explicit config account wins;
// otherwise a single linked account is auto-selected. Zero or several linked
// accounts is an operator decision the channel cannot make for them.
func (c *SecMsgChannel) resolveAccount(cl *smclient.Client, service string) (string, error) {
	if c.wantAccount != "" {
		return c.wantAccount, nil
	}
	all, err := cl.StatusAll(c.ctx)
	if err != nil {
		return "", fmt.Errorf("status: %w", err)
	}
	var linked []string
	for _, s := range all.Accounts {
		if s.Linked {
			linked = append(linked, s.Account)
		}
	}
	switch len(linked) {
	case 1:
		return linked[0], nil
	case 0:
		return "", fmt.Errorf("%s daemon has no linked account; link one via the WebUI", service)
	default:
		return "", fmt.Errorf("%s daemon has multiple accounts %v; set \"account\" in config", service, linked)
	}
}

func (c *SecMsgChannel) handleEnvelope(env *schema.Envelope) {
	if env == nil || env.Method != schema.MethodMessage {
		return
	}
	var m schema.MessageParams
	if err := json.Unmarshal(env.Params, &m); err != nil {
		logger.WarnCF("secmsg", "Failed to decode message", map[string]any{
			"channel": c.Name(),
			"error":   err.Error(),
		})
		return
	}
	c.handleMessage(m)
}

// handleMessage maps an inbound secmsg message onto the bus. Sync copies of our
// own sends (From.Self) and non-text events (edits, reactions, receipts) are
// ignored so the agent only ever sees genuine remote text.
func (c *SecMsgChannel) handleMessage(m schema.MessageParams) {
	if m.From.Self {
		return
	}
	if m.Type != "" && m.Type != schema.MessageTypeText {
		return
	}

	platform := c.service
	if platform == "" {
		platform = c.Name()
	}
	sender := bus.SenderInfo{
		Platform:    platform,
		PlatformID:  m.From.ID,
		CanonicalID: identity.BuildCanonicalID(platform, m.From.ID),
		DisplayName: m.From.Name,
	}
	// Reject before materializing attachments so disallowed senders cost nothing.
	// Logged at WARN with the copy-paste-ready canonical ID so an operator can
	// discover a sender's identifier (services differ: UUID, phone, …) simply by
	// messaging the account once and reading this line, then adding it to
	// allow_from.
	if !c.IsAllowedSender(sender) {
		logger.WarnCF("secmsg", "Message from unauthorized sender — add to allow_from to permit", map[string]any{
			"channel":      c.Name(),
			"allow_from":   sender.CanonicalID,
			"platform_id":  sender.PlatformID,
			"display_name": sender.DisplayName,
		})
		return
	}

	// 1:1 messages address our own account (To.Self); a group message addresses
	// the group itself, whose hex ID is the reply target.
	isGroup := !m.To.Self && m.To.ID != ""
	peerKind, peerID, chatID := "direct", m.From.ID, m.From.ID
	if isGroup {
		peerKind, peerID, chatID = "group", m.To.ID, groupChatPrefix+m.To.ID
	}

	messageID := strconv.FormatUint(m.Timestamp, 10)
	scope := channels.BuildMediaScope(c.Name(), chatID, messageID)

	content := m.Body
	var mediaRefs []string
	store := c.GetMediaStore()
	for _, a := range m.Attachments {
		if a.LocalPath == "" {
			continue
		}
		// The daemon already wrote the file; register the path (no download).
		ref := a.LocalPath
		if store != nil {
			if r, err := store.Store(a.LocalPath, media.MediaMeta{
				Filename:    a.FileName,
				ContentType: a.ContentType,
				Source:      "secmsg",
			}, scope); err == nil {
				ref = r
			}
		}
		mediaRefs = append(mediaRefs, ref)
		if a.Caption != "" {
			content = appendLine(content, a.Caption)
		}
		content = appendLine(content, "[attachment: "+attachmentLabel(a)+"]")
	}

	// secmsg carries no mention metadata, so group gating is prefix/permissive
	// only (never mention-based).
	if isGroup {
		respond, stripped := c.ShouldRespondInGroup(false, content)
		if !respond {
			return
		}
		content = stripped
	}

	metadata := map[string]string{
		"service":  m.Service,
		"account":  m.Account,
		"is_group": strconv.FormatBool(isGroup),
	}

	c.HandleMessage(c.ctx, bus.Peer{Kind: peerKind, ID: peerID},
		messageID, m.From.ID, chatID, content, mediaRefs, metadata, sender)
}

func (c *SecMsgChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}
	if msg.Content == "" {
		return nil
	}
	c.mu.RLock()
	cl, service, account := c.client, c.service, c.account
	c.mu.RUnlock()
	if cl == nil {
		// Not yet connected (still dialing/backing off); the caller may retry.
		return channels.ErrNotRunning
	}

	if groupID, ok := decodeGroupChatID(msg.ChatID); ok {
		if err := cl.SendGroupMessage(ctx, service, account, groupID, msg.Content); err != nil {
			return classifySendErr(err)
		}
		return nil
	}
	if err := cl.SendMessage(ctx, service, account, msg.ChatID, msg.Content); err != nil {
		return classifySendErr(err)
	}
	return nil
}

// classifySendErr maps a daemon send error to a channel sentinel. A stealth
// rejection (the account is receive-only) is an expected operator choice, not a
// fault, so it maps to ErrReceiveOnly (logged at INFO, no retry); everything
// else is a permanent ErrSendFailed. The underlying error is preserved in the
// message for diagnostics.
func classifySendErr(err error) error {
	code, ok := smclient.RPCErrorCode(err)
	return sendErrForCode(err, code, ok)
}

// sendErrForCode is the pure mapping from a (possibly absent) daemon RPC error
// code to a channel sentinel, split out so it can be unit-tested without a live
// daemon error.
func sendErrForCode(err error, code int, hasCode bool) error {
	if hasCode && code == schema.ErrCodeStealth {
		return fmt.Errorf("%w: %v", channels.ErrReceiveOnly, err)
	}
	return fmt.Errorf("%w: %v", channels.ErrSendFailed, err)
}

// decodeGroupChatID reports whether a ChatID targets a group and returns the
// group id. It is the exact inverse of the groupChatPrefix encoding applied on
// inbound, so Send and the inbound mapping stay symmetric.
func decodeGroupChatID(chatID string) (string, bool) {
	return strings.CutPrefix(chatID, groupChatPrefix)
}

// linkDeviceName is the label shown on the phone for the linked device.
const linkDeviceName = "ClawEh"

// RequestLink starts device linking and returns the pairing reply, whose URI is
// the QR payload to render. It dials a short-lived client so linking works even
// when the account is not yet linked (and the run loop is still backing off).
func (c *SecMsgChannel) RequestLink(ctx context.Context) (*schema.LinkReply, error) {
	cl, err := smclient.Dial(c.addr, smclient.WithTimeout(dialTimeout))
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer cl.Close()
	return cl.LinkRequest(ctx, c.linkAccount(), linkDeviceName)
}

// LinkState reports current pairing status for the account (pending/complete/error).
func (c *SecMsgChannel) LinkState(ctx context.Context) (*schema.LinkReply, error) {
	cl, err := smclient.Dial(c.addr, smclient.WithTimeout(dialTimeout))
	if err != nil {
		return nil, fmt.Errorf("dial: %w", err)
	}
	defer cl.Close()
	return cl.LinkStatus(ctx, c.linkAccount())
}

// linkAccount is the account identifier used for link RPCs — the configured one,
// or the resolved account once connected.
func (c *SecMsgChannel) linkAccount() string {
	if c.wantAccount != "" {
		return c.wantAccount
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.account
}

// appendLine joins a new line onto existing content, tolerating an empty base.
func appendLine(base, line string) string {
	if base == "" {
		return line
	}
	return base + "\n" + line
}

// attachmentLabel picks the most descriptive human label for an attachment.
func attachmentLabel(a schema.Attachment) string {
	if a.FileName != "" {
		return a.FileName
	}
	if a.ContentType != "" {
		return a.ContentType
	}
	return "file"
}
