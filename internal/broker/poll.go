package broker

import (
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/AnkushinDaniil/noti/internal/telegram"
)

const (
	minBackoff = 1 * time.Second
	maxBackoff = 60 * time.Second
)

// pollUpdates is the single getUpdates consumer. It long-polls Telegram,
// resolves tickets from callbacks/replies, advances and persists the offset,
// and backs off on errors. It never returns until stop is closed.
func (b *broker) pollUpdates(stop <-chan struct{}) {
	offsetPath := filepath.Join(b.dataDir, "getUpdates.offset")
	offset := readOffset(offsetPath)
	backoff := minBackoff

	for {
		select {
		case <-stop:
			return
		default:
		}

		updates, err := b.tg.GetUpdates(offset, pollTimeoutSec)
		if err != nil {
			b.setConnected(false)
			if errors.Is(err, telegram.ErrConflict) {
				log.Printf("broker: getUpdates 409 (another consumer?); backing off %s", backoff)
			} else {
				log.Printf("broker: getUpdates error: %v; backing off %s", err, backoff)
			}
			if !sleepOrStop(stop, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}

		b.setConnected(true)
		backoff = minBackoff

		allow := b.allowSet()
		for _, u := range updates {
			b.handleUpdate(u, allow)
			if u.UpdateID+1 > offset {
				offset = u.UpdateID + 1
			}
		}
		if len(updates) > 0 {
			writeOffset(offsetPath, offset)
		}
	}
}

// handleUpdate resolves at most one ticket from a single update, enforcing the
// allow-set on the originating chat id.
func (b *broker) handleUpdate(u telegram.Update, allow map[string]bool) {
	if u.CallbackQuery != nil {
		b.handleCallback(u.CallbackQuery, allow)
		return
	}
	if u.Message != nil {
		b.handleReply(u.Message, allow)
	}
}

// handleCallback resolves a ticket from an inline-keyboard button press.
func (b *broker) handleCallback(cb *telegram.CallbackQuery, allow map[string]bool) {
	chatID := ""
	if cb.Message != nil {
		chatID = strconv.FormatInt(cb.Message.Chat.ID, 10)
	}
	if !allowed(allow, chatID) {
		return
	}
	ticketID, idx, ok := parseCallbackData(cb.Data)
	if !ok {
		_ = b.tg.AnswerCallback(cb.ID)
		return
	}
	t := b.reg.get(ticketID)
	if t == nil {
		_ = b.tg.AnswerCallback(cb.ID)
		return
	}
	answer := ""
	if idx >= 0 && idx < len(t.options) {
		answer = t.options[idx]
	}
	if t.resolve(answer) {
		_ = b.tg.AnswerCallback(cb.ID)
		if cb.Message != nil {
			_ = b.tg.EditMessageText(chatID, cb.Message.MessageID,
				fmt.Sprintf("#%s -> %s", ticketID, answer))
		}
	} else {
		_ = b.tg.AnswerCallback(cb.ID)
	}
}

// handleReply resolves a ticket from a text reply: first by reply-to message
// id, then by the single-pending-ticket-for-chat fallback.
func (b *broker) handleReply(msg *telegram.Message, allow map[string]bool) {
	chatID := strconv.FormatInt(msg.Chat.ID, 10)
	if !allowed(allow, chatID) {
		return
	}
	if msg.ReplyToMessage != nil {
		if t := b.reg.byMessageID(msg.ReplyToMessage.MessageID); t != nil {
			t.resolve(msg.Text)
			return
		}
	}
	if t := b.reg.pendingForChat(chatID); t != nil {
		t.resolve(msg.Text)
	}
}

// allowed reports whether chatID is in the allow-set. An empty allow-set
// permits all chats (no configured chat ids yet).
func allowed(allow map[string]bool, chatID string) bool {
	if len(allow) == 0 {
		return true
	}
	return allow[chatID]
}

// parseCallbackData parses "noti:<ticket>:<idx>" into its parts.
func parseCallbackData(data string) (ticketID string, idx int, ok bool) {
	const prefix = "noti:"
	if !strings.HasPrefix(data, prefix) {
		return "", 0, false
	}
	rest := data[len(prefix):]
	sep := strings.LastIndex(rest, ":")
	if sep < 0 {
		return "", 0, false
	}
	ticketID = rest[:sep]
	n, err := strconv.Atoi(rest[sep+1:])
	if err != nil {
		return "", 0, false
	}
	return ticketID, n, true
}

// nextBackoff doubles backoff up to maxBackoff.
func nextBackoff(d time.Duration) time.Duration {
	d *= 2
	if d > maxBackoff {
		return maxBackoff
	}
	return d
}

// sleepOrStop waits for d or until stop is closed. Returns false if stopped.
func sleepOrStop(stop <-chan struct{}, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-stop:
		return false
	case <-t.C:
		return true
	}
}

// readOffset reads the persisted getUpdates offset, defaulting to 0.
func readOffset(path string) int {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0
	}
	n, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0
	}
	return n
}

// writeOffset persists the getUpdates offset.
func writeOffset(path string, offset int) {
	if err := os.WriteFile(path, []byte(strconv.Itoa(offset)), 0o644); err != nil {
		log.Printf("broker: persist offset failed: %v", err)
	}
}
