package telegram

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/mymmrac/telego"
	th "github.com/mymmrac/telego/telegohandler"
	tu "github.com/mymmrac/telego/telegoutil"

	"github.com/PivotLLM/ClawEh/pkg/bus"
	"github.com/PivotLLM/ClawEh/pkg/channels"
	"github.com/PivotLLM/ClawEh/pkg/commands"
	"github.com/PivotLLM/ClawEh/pkg/config"
	"github.com/PivotLLM/ClawEh/pkg/identity"
	"github.com/PivotLLM/ClawEh/pkg/logger"
	"github.com/PivotLLM/ClawEh/pkg/media"
	"github.com/PivotLLM/ClawEh/pkg/utils"
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
	// box-drawing line to match what Slack does (see pkg/channels/slack/mrkdwn.go).
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
// Slack channel (see pkg/channels/slack/mrkdwn.go).
const hRuleSubstitute = "──────────────────────────────"

// pollExitTimeout bounds how long Stop() will block waiting for telego's
// long-poll goroutine to exit. var (not const) so tests can shorten it.
var pollExitTimeout = 10 * time.Second

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
		return nil, fmt.Errorf("telegram bot token is required")
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
	opts = append(opts, telego.WithLogger(logger.NewLogger("telego").WithContentSensitive()))

	bot, err := telego.NewBot(botCfg.Token, opts...)
	if err != nil {
		return nil, fmt.Errorf("failed to create telegram bot: %w", err)
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

	return &TelegramChannel{
		BaseChannel:    base,
		bot:            bot,
		placeholderCfg: botCfg.Placeholder,
		coalesceCfg:    botCfg.Coalesce,
		chatIDs:        make(map[string]int64),
	}, nil
}

func (c *TelegramChannel) Start(ctx context.Context) error {
	logger.InfoC("telegram", "Starting Telegram bot (polling mode)...")

	c.ctx, c.cancel = context.WithCancel(ctx)
	c.stopOnce = sync.Once{}

	if c.coalesceCfg.IsEnabled() {
		c.coalescer = newMessageCoalescer(c.coalesceCfg, c.dispatchCoalesced)
	} else {
		c.coalescer = nil
	}

	rawUpdates, err := c.bot.UpdatesViaLongPolling(c.ctx, &telego.GetUpdatesParams{
		Timeout: 30,
	})
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to start long polling: %w", err)
	}

	updates, pollDone := watchLongPoll(c.ctx, rawUpdates)
	c.pollDone = pollDone

	bh, err := th.NewBotHandler(c.bot, updates)
	if err != nil {
		c.cancel()
		return fmt.Errorf("failed to create bot handler: %w", err)
	}
	c.bh = bh

	bh.HandleMessage(func(ctx *th.Context, message telego.Message) error {
		return c.handleMessage(ctx, &message)
	}, th.AnyMessage())

	c.SetRunning(true)
	logger.InfoCF("telegram", "Telegram bot connected", map[string]any{
		"username": c.bot.Username(),
	})

	c.startCommandRegistration(c.ctx, commands.BuiltinDefinitions())

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
			_ = c.bh.StopWithContext(ctx)
		}

		// Block until telego's long-poll goroutine has actually exited.
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

// watchLongPoll relays telego's long-poll updates channel through a goroutine
// we own. The returned done channel is closed only after the upstream channel
// is closed — which telego does from a defer inside doLongPolling — giving
// Stop() a reliable signal that the long-poll goroutine has exited. Once ctx
// is cancelled the relay drains the upstream so doLongPolling isn't blocked
// on send.
func watchLongPoll(ctx context.Context, src <-chan telego.Update) (<-chan telego.Update, chan struct{}) {
	dst := make(chan telego.Update, 1)
	done := make(chan struct{})
	go func() {
		defer close(done)
		defer close(dst)
		for {
			select {
			case <-ctx.Done():
				for range src {
				}
				return
			case u, ok := <-src:
				if !ok {
					return
				}
				select {
				case dst <- u:
				case <-ctx.Done():
					for range src {
					}
					return
				}
			}
		}
	}()
	return dst, done
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
			smallerLen := int(float64(4096) * ratio * 0.95) // 5% safety margin
			if smallerLen < 100 {
				smallerLen = 100
			}
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
	_ = c.bot.SendChatAction(ctx, action)

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
				_ = c.bot.SendChatAction(typingCtx, a)
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

	return fmt.Sprintf("%d", pMsg.MessageID), nil
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

		file, err := os.Open(localPath)
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

		file.Close()

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
		return fmt.Errorf("message is nil")
	}

	user := message.From
	if user == nil {
		return fmt.Errorf("message sender (user) is nil")
	}

	platformID := fmt.Sprintf("%d", user.ID)
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

	chatIDStr := fmt.Sprintf("%d", chatID)
	messageIDStr := fmt.Sprintf("%d", message.MessageID)
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
	compositeChatID := fmt.Sprintf("%d", chatID)
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
	peerID := fmt.Sprintf("%d", user.ID)
	if message.Chat.Type != "private" {
		peerKind = "group"
		peerID = compositeChatID
	}

	peer := bus.Peer{Kind: peerKind, ID: peerID}

	metadata := map[string]string{
		"user_id":    fmt.Sprintf("%d", user.ID),
		"username":   user.Username,
		"first_name": user.FirstName,
		"is_group":   fmt.Sprintf("%t", message.Chat.Type != "private"),
	}

	// Set parent_peer metadata for per-topic agent binding.
	if message.Chat.IsForum && threadID != 0 {
		metadata["parent_peer_kind"] = "topic"
		metadata["parent_peer_id"] = fmt.Sprintf("%d", threadID)
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
	idx := strings.Index(chatID, "/")
	if idx == -1 {
		cid, err := strconv.ParseInt(chatID, 10, 64)
		return cid, 0, err
	}
	cid, err := strconv.ParseInt(chatID[:idx], 10, 64)
	if err != nil {
		return 0, 0, err
	}
	tid, err := strconv.Atoi(chatID[idx+1:])
	if err != nil {
		return 0, 0, fmt.Errorf("invalid thread ID in chat ID %q: %w", chatID, err)
	}
	return cid, tid, nil
}

// wrapMarkdownTables detects contiguous blocks of pipe-delimited markdown table rows and
// wraps each block in triple-backtick fences so the existing code block handler renders
// them as monospace <pre><code> in Telegram. Lines already inside a code fence are skipped.
// Column widths are normalized so header, separator, and data cells all align.
func wrapMarkdownTables(text string) string {
	lines := strings.Split(text, "\n")
	var out []string
	inFence := false
	tableStart := -1

	// splitTableRow splits a pipe-delimited row into trimmed cell strings.
	// The row is expected to start and end with '|'.
	splitTableRow := func(line string) []string {
		trimmed := strings.TrimSpace(line)
		// Strip leading and trailing '|'.
		trimmed = strings.TrimPrefix(trimmed, "|")
		trimmed = strings.TrimSuffix(trimmed, "|")
		parts := strings.Split(trimmed, "|")
		cells := make([]string, len(parts))
		for i, p := range parts {
			cells[i] = strings.TrimSpace(p)
		}
		return cells
	}

	// isSeparatorCell returns true when a cell is a markdown table separator:
	// dashes optionally bracketed by alignment colons (---, :---, ---:, :---:).
	isSeparatorCell := func(cell string) bool {
		if len(cell) == 0 {
			return false
		}
		hasDash := false
		for _, ch := range cell {
			switch ch {
			case '-':
				hasDash = true
			case ':':
				// alignment marker, allowed
			default:
				return false
			}
		}
		return hasDash
	}

	// isSeparatorRow returns true when every cell in the row is a separator cell.
	isSeparatorRow := func(cells []string) bool {
		if len(cells) == 0 {
			return false
		}
		for _, c := range cells {
			if !isSeparatorCell(c) {
				return false
			}
		}
		return true
	}

	// normalizeTableBlock rewrites a slice of raw table lines so all columns are
	// padded to a uniform width and the separator row uses that many dashes.
	normalizeTableBlock := func(tableLines []string) []string {
		// Parse all rows.
		parsed := make([][]string, len(tableLines))
		for i, l := range tableLines {
			parsed[i] = splitTableRow(l)
		}

		// Determine max column count.
		maxCols := 0
		for _, cells := range parsed {
			if len(cells) > maxCols {
				maxCols = len(cells)
			}
		}
		if maxCols == 0 {
			return tableLines
		}

		// Find max content width per column, considering only non-separator rows.
		colWidth := make([]int, maxCols)
		for colIdx := range colWidth {
			colWidth[colIdx] = 3 // minimum width matches "---"
		}
		for _, cells := range parsed {
			if isSeparatorRow(cells) {
				continue
			}
			for colIdx, cell := range cells {
				if colIdx >= maxCols {
					break
				}
				if w := utf8.RuneCountInString(cell); w > colWidth[colIdx] {
					colWidth[colIdx] = w
				}
			}
		}

		// Rebuild each row with normalized widths.
		result := make([]string, len(tableLines))
		for i, cells := range parsed {
			rebuiltCells := make([]string, maxCols)
			if isSeparatorRow(cells) {
				for colIdx := range rebuiltCells {
					rebuiltCells[colIdx] = strings.Repeat("-", colWidth[colIdx])
				}
			} else {
				for colIdx := range rebuiltCells {
					var cell string
					if colIdx < len(cells) {
						cell = cells[colIdx]
					}
					// Pad with trailing spaces to colWidth (rune count, not bytes).
					rebuiltCells[colIdx] = cell + strings.Repeat(" ", colWidth[colIdx]-utf8.RuneCountInString(cell))
				}
			}
			result[i] = "| " + strings.Join(rebuiltCells, " | ") + " |"
		}
		return result
	}

	flush := func(end int) {
		if tableStart < 0 {
			return
		}
		normalized := normalizeTableBlock(lines[tableStart:end])
		out = append(out, "```")
		out = append(out, normalized...)
		out = append(out, "```")
		tableStart = -1
	}

	isTableRow := func(line string) bool {
		trimmed := strings.TrimSpace(line)
		return strings.HasPrefix(trimmed, "|") && strings.HasSuffix(trimmed, "|")
	}

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "```") {
			if inFence {
				inFence = false
				out = append(out, line)
				continue
			}
			// Entering a fence — flush any open table block first.
			flush(i)
			inFence = true
			out = append(out, line)
			continue
		}

		if inFence {
			out = append(out, line)
			continue
		}

		if isTableRow(line) {
			if tableStart < 0 {
				tableStart = i
			}
			// Defer appending; the block will be flushed when it ends.
			continue
		}

		// Not a table row — flush any open table block.
		flush(i)
		out = append(out, line)
	}

	// Flush any trailing table block.
	flush(len(lines))

	return strings.Join(out, "\n")
}

func markdownToTelegramHTML(text string) string {
	if text == "" {
		return ""
	}

	text = wrapMarkdownTables(text)

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
