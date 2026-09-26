package telegram

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/mymmrac/telego"
	ta "github.com/mymmrac/telego/telegoapi"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"
	"github.com/tenebris-tech/alerter"

	"github.com/PivotLLM/ClawEh/bus"
	"github.com/PivotLLM/ClawEh/channels"
	"github.com/PivotLLM/ClawEh/commands"
	"github.com/PivotLLM/ClawEh/config"
	"github.com/PivotLLM/ClawEh/identity"
	"github.com/PivotLLM/ClawEh/logger"
	"github.com/PivotLLM/ClawEh/mdhelper"
	"github.com/PivotLLM/ClawEh/media"
	"github.com/PivotLLM/ClawEh/utils"
)

var (
	reHeading    = regexp.MustCompile(`(?m)^#{1,6}\s+(.+)$`)
	reBlockquote = regexp.MustCompile(`(?m)^>\s*(.*)$`)
	reLink       = regexp.MustCompile(`\[([^\]]+)\]\(([^)]+)\)`)
	reBoldStar   = regexp.MustCompile(`\*\*(.+?)\*\*`)
	reBoldUnder  = regexp.MustCompile(`__(.+?)__`)
	reItalic     = regexp.MustCompile(`_([^_]+)_`)
	reStrike     = regexp.MustCompile(`~~(.+?)~~`)
	reListItem   = regexp.MustCompile(`(?m)^[-*]\s+`)
	reCodeBlock  = regexp.MustCompile("```[\\w]*\\n?([\\s\\S]*?)```")
	reInlineCode = regexp.MustCompile("`([^`]+)`")

	// Horizontal rules: ---, ***, or ___ alone on a line. Telegram has no
	// horizontal-rule primitive, so we substitute these with a visible
	// box-drawing line to match what Slack does (see channels/slack/mrkdwn.go).
	reHRule = regexp.MustCompile(`(?m)^[ \t]*[-*_]{3,}[ \t]*$`)

	// Runs of consecutive hRuleSubstitute lines separated only by blank lines.
	// Used to collapse stacked rules (e.g. when a display payload itself ends
	// with a thematic break and displayBody adds its own closing fence) down
	// to a single visible rule.
	reHRuleRun = regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(hRuleSubstitute) + `(?:\n[ \t]*)+` + regexp.QuoteMeta(hRuleSubstitute) + `(?:(?:\n[ \t]*)+` + regexp.QuoteMeta(hRuleSubstitute) + `)*$`)
)

// hRuleSubstitute renders a CommonMark thematic break (---, ***, ___ alone on
// a line) as a visible horizontal-rule-like line in Telegram, which has no
// native horizontal-rule primitive. Matches the glyph and length used by the
// Slack channel (see channels/slack/mrkdwn.go).
const hRuleSubstitute = "──────────────────────────────"

// pollExitTimeout bounds how long Stop() will block waiting for telego's
// long-poll goroutine to exit. var (not const) so tests can shorten it.
var pollExitTimeout = 10 * time.Second

// pollTimeoutSeconds is the getUpdates long-poll timeout Telegram holds each
// request open for.
const pollTimeoutSeconds = 30

// pollBuffer is the capacity of the updates channel between the poll loop and
// the bot handler.
const pollBuffer = 100

// isTransientPollError reports whether a telego log message describes a
// recoverable long-poll failure — a transient Telegram 5xx or a network blip
// during getUpdates — rather than a genuine fault. telego logs these itself via
// Errorf at ERROR; they are demoted to WARN because pollUpdates retries them
// and no updates are lost (the offset is not advanced on a failed call).
// Wired into the telego logger via WithErrorDowngrade.
func isTransientPollError(msg string) bool {
	m := strings.ToLower(msg)
	// Restrict to the long-poll update path so unrelated telego errors are
	// never downgraded.
	if !strings.Contains(m, "getupdates") {
		return false
	}
	// A transient HTTP 5xx from Telegram's API (telego formats these as
	// "internal server error: <code>"), or a transport-level blip that means the
	// request never reached Telegram.
	if strings.Contains(m, "internal server error: 5") {
		return true
	}
	for _, s := range []string{
		"bad gateway",
		"gateway timeout",
		"service unavailable",
		"i/o timeout",
		"tls handshake timeout",
		"context deadline exceeded",
		"connection reset",
		"connection refused",
		"network is unreachable",
		"no route to host",
		"unexpected eof",
	} {
		if strings.Contains(m, s) {
			return true
		}
	}
	return false
}

type TelegramChannel struct {
	*channels.BaseChannel
	bot            *telego.Bot
	bh             *th.BotHandler
	placeholderCfg config.PlaceholderConfig
	coalesceCfg    config.CoalesceConfig
	coalescer      *messageCoalescer
	chatIDs        map[string]int64
	ctx            context.Context
	cancel         context.CancelFunc

	// pollDone is closed when the goroutine wrapping telego's long-poll
	// updates channel returns, which only happens after telego's internal
	// doLongPolling goroutine has exited and closed the upstream channel.
	// Stop() waits on this to avoid the next Start() racing into a 409
	// "terminated by other getUpdates request" against an in-flight poll.
	pollDone chan struct{}
	stopOnce sync.Once

	registerFunc     func(context.Context, []commands.Definition) error
	commandRegCancel context.CancelFunc
}

// NewTelegramChannelFromConfig creates a TelegramChannel from a TelegramBotConfig.
// The channel name is derived from botCfg.ChannelName().
func NewTelegramChannelFromConfig(botCfg config.TelegramBotConfig, b *bus.MessageBus) (*TelegramChannel, error) {
	if botCfg.Token == "" {
		return nil, errors.New("telegram bot token is required")
	}
	var opts []telego.BotOption

	if botCfg.Proxy != "" {
		proxyURL, parseErr := url.Parse(botCfg.Proxy)
		if parseErr != nil {
			return nil, fmt.Errorf("invalid proxy URL %q: %w", botCfg.Proxy, parseErr)
		}
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyURL(proxyURL),
			},
		}))
	} else if os.Getenv("HTTP_PROXY") != "" || os.Getenv("HTTPS_PROXY") != "" {
		opts = append(opts, telego.WithHTTPClient(&http.Client{
			Transport: &http.Transport{
				Proxy: http.ProxyFromEnvironment,
			},
		}))
	}

	if baseURL := strings.TrimRight(strings.TrimSpace(botCfg.BaseURL), "/"); baseURL != "" {
		opts = append(opts, telego.WithAPIServer(baseURL))
	}
	channelName := botCfg.ChannelName()
	base := channels.NewBaseChannel(
		channelName,
		botCfg,
		b,
		botCfg.AllowFrom,
		channels.WithMaxMessageLength(4000),
		channels.WithGroupTrigger(botCfg.GroupTrigger),
		channels.WithReasoningChannelID(botCfg.ReasoningChannelID),
	)
	ch := &TelegramChannel{
		BaseChannel:    base,
		placeholderCfg: botCfg.Placeholder,
		coalesceCfg:    botCfg.Coalesce,
		chatIDs:        make(map[string]int64),
	}

	opts = append(opts, telego.WithLogger(
		logger.NewLogger("telego").WithContentSensitive().
			WithErrorDowngrade(isTransientPollError),
	))

	bot, err := telego.NewBot(botCfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
	}
	ch.bot = bot

	return ch, nil
}

// pollAlertMsgLimit bounds the telego message carried in a polling alert.
const pollAlertMsgLimit = 200

// alertPollFailure raises an alert for a long-poll failure that no retry can
// fix: Telegram rejecting the bot token (401). The poll loop keeps retrying at
// its slowest rate in case the token is restored; the alerter de-duplicates
// the repeats on the channel name (EventID, filled in by Alert).
func (c *TelegramChannel) alertPollFailure(msg string) {
	if r := []rune(msg); len(r) > pollAlertMsgLimit {
		msg = string(r[:pollAlertMsgLimit]) + "..."
	}
	c.Alert(alerter.Alert{
		Title:       "Telegram polling failed",
		Description: c.Name() + ": " + msg,
	})
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot (polling mode)...")

	pollCtx, cancel := context.WithCancel(ctx)
	c.ctx, c.cancel = pollCtx, cancel
	c.stopOnce = sync.Once{}

	if c.coalesceCfg.IsEnabled() {
		c.coalescer = newMessageCoalescer(c.coalesceCfg, c.dispatchCoalesced)
	} else {
		c.coalescer = nil
	}

	updates := make(chan telego.Update, pollBuffer)
	pollDone := make(chan struct{})
	c.pollDone = pollDone
	go func() {
		defer close(pollDone)
		c.pollUpdates(pollCtx, updates)
	}()

	bh, err := th.NewBotHandler(c.bot, updates)
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to create bot handler: %w", err)
	}
	c.bh = bh

	bh.HandleMessage(func(ctx *th.Context, message telego.Message) error { //nolint:contextcheck // th.Context is telego's handler context (it embeds context.Context); the linter does not recognise it
		return c.handleMessage(ctx, &message)
	}, th.AnyMessage())

	c.SetRunning(true)
	logger.InfoCF("telegram", "Telegram bot connected", map[string]any{
		"username": c.bot.Username(),
	})

	c.startCommandRegistration(pollCtx, commands.BuiltinDefinitions())

	go func() {
		if err = bh.Start(); err != nil {
			logger.ErrorCF("telegram", "Bot handler failed", map[string]any{
				"error": err.Error(),
			})
		}
	}()

	return nil
}

func (c *TelegramChannel) Stop(ctx context.Context) error {
	c.stopOnce.Do(func() {
		logger.InfoC("telegram", "Stopping Telegram bot...")
		c.SetRunning(false)

		// Flush any buffered (coalescing) messages before cancelling the context
		// so the dispatch path can still publish them to the bus.
		if c.coalescer != nil {
			c.coalescer.flushAll()
		}

		// Cancel first so any in-flight getUpdates / handler context unblocks.
		if c.cancel != nil {
			c.cancel()
		}
		if c.commandRegCancel != nil {
			c.commandRegCancel()
		}
		if c.bh != nil {
			if err := c.bh.StopWithContext(ctx); err != nil {
				logger.DebugCF("telegram", "Bot handler stop returned error", map[string]any{
					"error": err.Error(),
				})
			}
		}

		// Block until the long-poll goroutine has actually exited.
		// Without this, the next Start() (e.g. during config reload) races
		// into a 409 "terminated by other getUpdates request" against an
		// in-flight HTTP poll on Telegram's side.
		if c.pollDone != nil {
			select {
			case <-c.pollDone:
			case <-time.After(pollExitTimeout):
				logger.WarnCF("telegram", "Timed out waiting for long-poll goroutine to exit", map[string]any{
					"timeout": pollExitTimeout.String(),
				})
			}
		}
	})

	return nil
}

// pollUpdates is the long-poll loop; it closes updates when it returns. It
// replaces telego's UpdatesViaLongPolling, which retries after a fixed delay,
// ignores Telegram's retry_after, and sleeps without watching ctx. Every
// failure is retried: a 429 waits the retry_after Telegram asked for plus
// channels.RetryAfterPadding, anything else backs off from
// channels.ConnRetryMin to channels.ConnRetryMax. Waits end at once when ctx is
// cancelled, so Stop() is not held up by a sleeping retry.
func (c *TelegramChannel) pollUpdates(ctx context.Context, updates chan<- telego.Update) {
	defer close(updates)
	params := &telego.GetUpdatesParams{Timeout: pollTimeoutSeconds}
	var backoff time.Duration
	for ctx.Err() == nil {
		batch, err := c.bot.GetUpdates(ctx, params)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			var wait time.Duration
			wait, backoff = pollRetryWait(err, backoff)
			c.pollFailed(err, wait)
			select {
			case <-ctx.Done():
				return
			case <-time.After(wait):
			}
			continue
		}
		backoff = 0
		c.ReportConnected()
		for _, u := range batch {
			if u.UpdateID < params.Offset {
				continue
			}
			params.Offset = u.UpdateID + 1
			select {
			case <-ctx.Done():
				return
			case updates <- u.WithContext(ctx):
			}
		}
	}
}

// pollRetryWait returns how long to wait after a failed getUpdates, and the
// backoff to carry into the next failure. A server-given retry_after is
// honoured, padded, and leaves the backoff where it was.
func pollRetryWait(err error, backoff time.Duration) (wait, next time.Duration) {
	var apiErr *ta.Error
	if errors.As(err, &apiErr) && apiErr.Parameters != nil && apiErr.Parameters.RetryAfter > 0 {
		return time.Duration(apiErr.Parameters.RetryAfter)*time.Second + channels.RetryAfterPadding, backoff
	}
	next = channels.NextConnRetry(backoff)
	return next, next
}

// pollFailed records a failed getUpdates. A rejected token (401) cannot be
// fixed by retrying, so it alerts at once; every other failure feeds the
// channel's outage tracker, which alerts only if the outage outlasts
// channels.ConnDownAlertAfter.
func (c *TelegramChannel) pollFailed(err error, wait time.Duration) {
	var apiErr *ta.Error
	if errors.As(err, &apiErr) && apiErr.ErrorCode == http.StatusUnauthorized {
		logger.ErrorCF("telegram", "Telegram rejected the bot token", map[string]any{
			"channel": c.Name(),
			"error":   err.Error(),
		})
		c.alertPollFailure(err.Error())
		return
	}
	logger.WarnCF("telegram", "Telegram poll failed; retrying", map[string]any{
		"channel": c.Name(),
		"error":   err.Error(),
		"retry":   wait.String(),
	})
	c.ReportConnFailure(err)
}

func (c *TelegramChannel) Send(ctx context.Context, msg bus.OutboundMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	chatID, threadID, err := parseTelegramChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
	}

	if msg.Content == "" {
		return nil
	}

	// The Manager already splits messages to ≤4000 chars (WithMaxMessageLength),
	// so msg.Content is guaranteed to be within that limit. We still need to
	// check if HTML expansion pushes it beyond Telegram's 4096-char API limit.
	replyToID := msg.ReplyToMessageID
	queue := []string{msg.Content}
	for len(queue) > 0 {
		chunk := queue[0]
		queue = queue[1:]

		htmlContent := markdownToTelegramHTML(chunk)

		if len([]rune(htmlContent)) > 4096 {
			ratio := float64(len([]rune(chunk))) / float64(len([]rune(htmlContent)))
			smallerLen := max(
				// 5% safety margin
				int(float64(4096)*ratio*0.95), 100)
			// Push sub-chunks back to the front of the queue for
			// re-validation instead of sending them blindly.
			subChunks := channels.SplitMessage(chunk, smallerLen)
			queue = append(subChunks, queue...)
			continue
		}

		if err := c.sendHTMLChunk(ctx, chatID, threadID, htmlContent, chunk, replyToID); err != nil {
			return err
		}
		// Only the first chunk should be a reply; subsequent chunks are normal messages.
		replyToID = ""
	}

	return nil
}

// sendHTMLChunk sends a single HTML message, falling back to the original
// markdown as plain text on parse failure so users never see raw HTML tags.
func (c *TelegramChannel) sendHTMLChunk(
	ctx context.Context, chatID int64, threadID int, htmlContent, mdFallback string, replyToID string,
) error {
	tgMsg := tu.Message(tu.ID(chatID), htmlContent)
	tgMsg.ParseMode = telego.ModeHTML
	tgMsg.MessageThreadID = threadID

	if replyToID != "" {
		if mid, parseErr := strconv.Atoi(replyToID); parseErr == nil {
			tgMsg.ReplyParameters = &telego.ReplyParameters{
				MessageID: mid,
			}
		}
	}

	if _, err := c.bot.SendMessage(ctx, tgMsg); err != nil {
		logger.ErrorCF("telegram", "HTML parse failed, falling back to plain text", map[string]any{
			"error": err.Error(),
		})
		tgMsg.Text = mdFallback
		tgMsg.ParseMode = ""
		if _, err = c.bot.SendMessage(ctx, tgMsg); err != nil {
			return fmt.Errorf("telegram send: %w", channels.ErrTemporary)
		}
	}
	return nil
}

// StartTyping implements channels.TypingCapable.
// It sends ChatAction(typing) immediately and then repeats every 4 seconds
// (Telegram's typing indicator expires after ~5s) in a background goroutine.
// The returned stop function is idempotent and cancels the goroutine.
func (c *TelegramChannel) StartTyping(ctx context.Context, chatID string) (func(), error) {
	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return func() {}, err
	}

	action := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
	action.MessageThreadID = threadID

	// Send the first typing action immediately
	if err := c.bot.SendChatAction(ctx, action); err != nil {
		logger.DebugCF("telegram", "Failed to send typing action", map[string]any{
			"chat_id": cid, "error": err.Error(),
		})
	}

	typingCtx, cancel := context.WithCancel(ctx)
	go func() {
		ticker := time.NewTicker(4 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-typingCtx.Done():
				return
			case <-ticker.C:
				a := tu.ChatAction(tu.ID(cid), telego.ChatActionTyping)
				a.MessageThreadID = threadID
				if err := c.bot.SendChatAction(typingCtx, a); err != nil {
					logger.DebugCF("telegram", "Failed to send typing action", map[string]any{
						"chat_id": cid, "error": err.Error(),
					})
				}
			}
		}
	}()

	return cancel, nil
}

// EditMessage implements channels.MessageEditor.
func (c *TelegramChannel) EditMessage(ctx context.Context, chatID string, messageID string, content string) error {
	cid, _, err := parseTelegramChatID(chatID)
	if err != nil {
		return err
	}
	mid, err := strconv.Atoi(messageID)
	if err != nil {
		return err
	}
	htmlContent := markdownToTelegramHTML(content)
	editMsg := tu.EditMessageText(tu.ID(cid), mid, htmlContent)
	editMsg.ParseMode = telego.ModeHTML
	_, err = c.bot.EditMessageText(ctx, editMsg)
	return err
}

// SendPlaceholder implements channels.PlaceholderCapable.
// It sends a placeholder message (e.g. "Thinking... 💭") that will later be
// edited to the actual response via EditMessage (channels.MessageEditor).
func (c *TelegramChannel) SendPlaceholder(ctx context.Context, chatID string) (string, error) {
	phCfg := c.placeholderCfg
	if !phCfg.Enabled {
		return "", nil
	}

	text := phCfg.Text
	if text == "" {
		text = "Thinking... 💭"
	}

	cid, threadID, err := parseTelegramChatID(chatID)
	if err != nil {
		return "", err
	}

	phMsg := tu.Message(tu.ID(cid), text)
	phMsg.MessageThreadID = threadID
	pMsg, err := c.bot.SendMessage(ctx, phMsg)
	if err != nil {
		return "", err
	}

	return strconv.Itoa(pMsg.MessageID), nil
}

// SendMedia implements the channels.MediaSender interface.
func (c *TelegramChannel) SendMedia(ctx context.Context, msg bus.OutboundMediaMessage) error {
	if !c.IsRunning() {
		return channels.ErrNotRunning
	}

	chatID, threadID, err := parseTelegramChatID(msg.ChatID)
	if err != nil {
		return fmt.Errorf("invalid chat ID %s: %w", msg.ChatID, channels.ErrSendFailed)
	}

	store := c.GetMediaStore()
	if store == nil {
		return fmt.Errorf("no media store available: %w", channels.ErrSendFailed)
	}

	for _, part := range msg.Parts {
		localPath, err := store.Resolve(part.Ref)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to resolve media ref", map[string]any{
				"ref":   part.Ref,
				"error": err.Error(),
			})
			continue
		}

		file, err := os.Open(localPath) //nolint:gosec // path comes from the media store's own ref map (FileMediaStore.Resolve)
		if err != nil {
			logger.ErrorCF("telegram", "Failed to open media file", map[string]any{
				"path":  localPath,
				"error": err.Error(),
			})
			continue
		}

		switch part.Type {
		case "image":
			params := &telego.SendPhotoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Photo:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendPhoto(ctx, params)
		case "audio":
			params := &telego.SendAudioParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Audio:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendAudio(ctx, params)
		case "video":
			params := &telego.SendVideoParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Video:           telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendVideo(ctx, params)
		default: // "file" or unknown types
			params := &telego.SendDocumentParams{
				ChatID:          tu.ID(chatID),
				MessageThreadID: threadID,
				Document:        telego.InputFile{File: file},
				Caption:         part.Caption,
			}
			_, err = c.bot.SendDocument(ctx, params)
		}

		utils.CloseQuietly(file)

		if err != nil {
			logger.ErrorCF("telegram", "Failed to send media", map[string]any{
				"type":  part.Type,
				"error": err.Error(),
			})
			return fmt.Errorf("telegram send media: %w", channels.ErrTemporary)
		}
	}

	return nil
}

func (c *TelegramChannel) handleMessage(ctx context.Context, message *telego.Message) error {
	if message == nil {
		return errors.New("message is nil")
	}

	user := message.From
	if user == nil {
		return errors.New("message sender (user) is nil")
	}

	platformID := strconv.FormatInt(user.ID, 10)
	sender := bus.SenderInfo{
		Platform:    "telegram",
		PlatformID:  platformID,
		CanonicalID: identity.BuildCanonicalID("telegram", platformID),
		Username:    user.Username,
		DisplayName: user.FirstName,
	}

	// check allowlist to avoid downloading attachments for rejected users
	if !c.IsAllowedSender(sender) {
		logger.DebugCF("telegram", "Message rejected by allowlist", map[string]any{
			"user_id": platformID,
		})
		return nil
	}

	chatID := message.Chat.ID
	c.chatIDs[platformID] = chatID

	content := ""
	mediaPaths := []string{}

	chatIDStr := strconv.FormatInt(chatID, 10)
	messageIDStr := strconv.Itoa(message.MessageID)
	scope := channels.BuildMediaScope("telegram", chatIDStr, messageIDStr)

	// Helper to register a local file with the media store
	storeMedia := func(localPath, filename string) string {
		if store := c.GetMediaStore(); store != nil {
			ref, err := store.Store(localPath, media.MediaMeta{
				Filename: filename,
				Source:   "telegram",
			}, scope)
			if err == nil {
				return ref
			}
		}
		return localPath // fallback: use raw path
	}

	if message.Text != "" {
		content += message.Text
	}

	if message.Caption != "" {
		if content != "" {
			content += "\n"
		}
		content += message.Caption
	}

	if len(message.Photo) > 0 {
		photo := message.Photo[len(message.Photo)-1]
		photoPath := c.downloadPhoto(ctx, photo.FileID)
		if photoPath != "" {
			mediaPaths = append(mediaPaths, storeMedia(photoPath, "photo.jpg"))
			if content != "" {
				content += "\n"
			}
			content += "[image: photo]"
		}
	}

	if message.Voice != nil {
		voicePath := c.downloadFile(ctx, message.Voice.FileID, ".ogg")
		if voicePath != "" {
			mediaPaths = append(mediaPaths, storeMedia(voicePath, "voice.ogg"))

			if content != "" {
				content += "\n"
			}
			content += "[voice]"
		}
	}

	if message.Audio != nil {
		audioPath := c.downloadFile(ctx, message.Audio.FileID, ".mp3")
		if audioPath != "" {
			mediaPaths = append(mediaPaths, storeMedia(audioPath, "audio.mp3"))
			if content != "" {
				content += "\n"
			}
			content += "[audio]"
		}
	}

	if message.Document != nil {
		docPath := c.downloadFile(ctx, message.Document.FileID, "")
		if docPath != "" {
			mediaPaths = append(mediaPaths, storeMedia(docPath, "document"))
			if content != "" {
				content += "\n"
			}
			content += "[file]"
		}
	}

	if content == "" {
		content = "[empty message]"
	}

	// In group chats, apply unified group trigger filtering
	if message.Chat.Type != "private" {
		isMentioned := c.isBotMentioned(message)
		if isMentioned {
			content = c.stripBotMention(content)
		}
		respond, cleaned := c.ShouldRespondInGroup(isMentioned, content)
		if !respond {
			return nil
		}
		content = cleaned
	}

	// For forum topics, embed the thread ID as "chatID/threadID" so replies
	// route to the correct topic and each topic gets its own session.
	// Only forum groups (IsForum) are handled; regular group reply threads
	// must share one session per group.
	compositeChatID := strconv.FormatInt(chatID, 10)
	threadID := message.MessageThreadID
	if message.Chat.IsForum && threadID != 0 {
		compositeChatID = fmt.Sprintf("%d/%d", chatID, threadID)
	}

	logFields := map[string]any{
		"sender_id": sender.CanonicalID,
		"chat_id":   compositeChatID,
		"thread_id": threadID,
	}
	if logger.GetLogMessageContent() {
		logFields["preview"] = utils.Truncate(content, 50)
	}
	logger.DebugCF("telegram", "Received message", logFields)

	peerKind := "direct"
	peerID := strconv.FormatInt(user.ID, 10)
	if message.Chat.Type != "private" {
		peerKind = "group"
		peerID = compositeChatID
	}

	peer := bus.Peer{Kind: peerKind, ID: peerID}

	metadata := map[string]string{
		"user_id":    strconv.FormatInt(user.ID, 10),
		"username":   user.Username,
		"first_name": user.FirstName,
		"is_group":   strconv.FormatBool(message.Chat.Type != "private"),
	}

	// Set parent_peer metadata for per-topic agent binding.
	if message.Chat.IsForum && threadID != 0 {
		metadata["parent_peer_kind"] = "topic"
		metadata["parent_peer_id"] = strconv.Itoa(threadID)
	}

	c.enqueue(coalescedMessage{
		messageID:  message.MessageID,
		peer:       peer,
		platformID: platformID,
		chatID:     compositeChatID,
		content:    content,
		media:      mediaPaths,
		metadata:   metadata,
		sender:     sender,
	})
	return nil
}

// enqueue routes a parsed message into the coalescer, or dispatches it
// immediately when coalescing is disabled. Bot commands bypass the buffer
// entirely: they must never be delayed or merged with surrounding text, and any
// pending buffered text for the sender is flushed first so it is processed
// before the command.
func (c *TelegramChannel) enqueue(m coalescedMessage) {
	if c.coalescer == nil {
		c.dispatchCoalesced(m)
		return
	}
	if _, isCmd := commands.ParseCommandName(m.content); isCmd {
		c.coalescer.flushKey(coalesceKey(m))
		c.dispatchCoalesced(m)
		return
	}
	c.coalescer.add(coalesceKey(m), m)
}

// dispatchCoalesced forwards a (possibly combined) message to the base channel
// for publishing. messageID is the anchor fragment's ID.
func (c *TelegramChannel) dispatchCoalesced(m coalescedMessage) {
	c.HandleMessage(c.ctx,
		m.peer,
		strconv.Itoa(m.messageID),
		m.platformID,
		m.chatID,
		m.content,
		m.media,
		m.metadata,
		m.sender,
	)
}

func (c *TelegramChannel) downloadPhoto(ctx context.Context, fileID string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get photo file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ".jpg")
}

func (c *TelegramChannel) downloadFileWithInfo(file *telego.File, ext string) string {
	if file.FilePath == "" {
		return ""
	}

	url := c.bot.FileDownloadURL(file.FilePath)
	logger.DebugCF("telegram", "File URL", map[string]any{"url": url})

	// Use FilePath as filename for better identification
	filename := file.FilePath + ext
	return utils.DownloadFile(url, filename, utils.DownloadOptions{
		LoggerPrefix: "telegram",
	})
}

func (c *TelegramChannel) downloadFile(ctx context.Context, fileID, ext string) string {
	file, err := c.bot.GetFile(ctx, &telego.GetFileParams{FileID: fileID})
	if err != nil {
		logger.ErrorCF("telegram", "Failed to get file", map[string]any{
			"error": err.Error(),
		})
		return ""
	}

	return c.downloadFileWithInfo(file, ext)
}

// parseTelegramChatID splits "chatID/threadID" into its components.
// Returns threadID=0 when no "/" is present (non-forum messages).
func parseTelegramChatID(chatID string) (int64, int, error) {
	before, after, ok := strings.Cut(chatID, "/")
	if !ok {
		cid, err := strconv.ParseInt(chatID, 10, 64)
		return cid, 0, err
	}
	cid, err := strconv.ParseInt(before, 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tid, err := strconv.Atoi(after)
	if err != nil {
		return 0, 0, fmt.Errorf("invalid thread ID in chat ID %q: %w", chatID, err)
	}
	return cid, tid, nil
}

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	text = mdhelper.FormatTables(text)

	codeBlocks := extractCodeBlocks(text)
	text = codeBlocks.text

	inlineCodes := extractInlineCodes(text)
	text = inlineCodes.text

	// Horizontal rules → a visible box-drawing line. Telegram has no native
	// horizontal-rule primitive, but display:true payloads use --- as a
	// CommonMark thematic break to fence the payload, so passing the line
	// through erases the fence visually; substitute a box-drawing line
	// instead (matching Slack).
	text = reHRule.ReplaceAllString(text, hRuleSubstitute)

	// Collapse runs of adjacent rules (separated only by blank lines) down to
	// a single rule. Display payloads that themselves end with a thematic
	// break would otherwise stack against the closing fence emitted by
	// displayBody.
	text = reHRuleRun.ReplaceAllString(text, hRuleSubstitute)

	text = reHeading.ReplaceAllString(text, "$1")

	text = reBlockquote.ReplaceAllString(text, "$1")

	text = escapeHTML(text)

	text = reLink.ReplaceAllString(text, `<a href="$2">$1</a>`)

	text = reBoldStar.ReplaceAllString(text, "<b>$1</b>")

	text = reBoldUnder.ReplaceAllString(text, "<b>$1</b>")

	text = reItalic.ReplaceAllStringFunc(text, func(s string) string {
		match := reItalic.FindStringSubmatch(s)
		if len(match) < 2 {
			return s
		}
		return "<i>" + match[1] + "</i>"
	})

	text = reStrike.ReplaceAllString(text, "<s>$1</s>")

	text = reListItem.ReplaceAllString(text, "• ")

	for i, code := range inlineCodes.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(text, fmt.Sprintf("\x00IC%d\x00", i), fmt.Sprintf("<code>%s</code>", escaped))
	}

	for i, code := range codeBlocks.codes {
		escaped := escapeHTML(code)
		text = strings.ReplaceAll(
			text,
			fmt.Sprintf("\x00CB%d\x00", i),
			fmt.Sprintf("<pre><code>%s</code></pre>", escaped),
		)
	}

	return text
}

type codeBlockMatch struct {
	text  string
	codes []string
}

func extractCodeBlocks(text string) codeBlockMatch {
	matches := reCodeBlock.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reCodeBlock.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00CB%d\x00", i)
		i++
		return placeholder
	})

	return codeBlockMatch{text: text, codes: codes}
}

type inlineCodeMatch struct {
	text  string
	codes []string
}

func extractInlineCodes(text string) inlineCodeMatch {
	matches := reInlineCode.FindAllStringSubmatch(text, -1)

	codes := make([]string, 0, len(matches))
	for _, match := range matches {
		codes = append(codes, match[1])
	}

	i := 0
	text = reInlineCode.ReplaceAllStringFunc(text, func(m string) string {
		placeholder := fmt.Sprintf("\x00IC%d\x00", i)
		i++
		return placeholder
	})

	return inlineCodeMatch{text: text, codes: codes}
}

func escapeHTML(text string) string {
	text = strings.ReplaceAll(text, "&", "&amp;")
	text = strings.ReplaceAll(text, "<", "&lt;")
	text = strings.ReplaceAll(text, ">", "&gt;")
	return text
}

// isBotMentioned checks if the bot is mentioned in the message via entities.
func (c *TelegramChannel) isBotMentioned(message *telego.Message) bool {
	text, entities := telegramEntityTextAndList(message)
	if text == "" || len(entities) == 0 {
		return false
	}

	botUsername := ""
	if c.bot != nil {
		botUsername = c.bot.Username()
	}
	runes := []rune(text)

	for _, entity := range entities {
		entityText, ok := telegramEntityText(runes, entity)
		if !ok {
			continue
		}

		switch entity.Type {
		case telego.EntityTypeMention:
			if botUsername != "" && strings.EqualFold(entityText, "@"+botUsername) {
				return true
			}
		case telego.EntityTypeTextMention:
			if botUsername != "" && entity.User != nil && strings.EqualFold(entity.User.Username, botUsername) {
				return true
			}
		case telego.EntityTypeBotCommand:
			if isBotCommandEntityForThisBot(entityText, botUsername) {
				return true
			}
		}
	}
	return false
}

func telegramEntityTextAndList(message *telego.Message) (string, []telego.MessageEntity) {
	if message.Text != "" {
		return message.Text, message.Entities
	}
	return message.Caption, message.CaptionEntities
}

func telegramEntityText(runes []rune, entity telego.MessageEntity) (string, bool) {
	if entity.Offset < 0 || entity.Length <= 0 {
		return "", false
	}
	end := entity.Offset + entity.Length
	if entity.Offset >= len(runes) || end > len(runes) {
		return "", false
	}
	return string(runes[entity.Offset:end]), true
}

func isBotCommandEntityForThisBot(entityText, botUsername string) bool {
	if !strings.HasPrefix(entityText, "/") {
		return false
	}
	command := strings.TrimPrefix(entityText, "/")
	if command == "" {
		return false
	}

	at := strings.IndexRune(command, '@')
	if at == -1 {
		// A bare /command delivered to this bot is intended for this bot.
		return true
	}

	mentionUsername := command[at+1:]
	if mentionUsername == "" || botUsername == "" {
		return false
	}
	return strings.EqualFold(mentionUsername, botUsername)
}

// stripBotMention removes the @bot mention from the content.
func (c *TelegramChannel) stripBotMention(content string) string {
	botUsername := c.bot.Username()
	if botUsername == "" {
		return content
	}
	// Case-insensitive replacement
	re := regexp.MustCompile(`(?i)@` + regexp.QuoteMeta(botUsername))
	content = re.ReplaceAllString(content, "")
	return strings.TrimSpace(content)
}
