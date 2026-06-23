// Package notify implements the hook notifier invoked by `noti notify <level>`.
// It reads a Claude hook JSON payload from an io.Reader, builds a human-friendly
// notification text, and delivers it via the broker (POST /notify, 5-second
// timeout) with a direct Telegram fallback when the broker is unreachable.
// Run always returns nil — the hook must never break a Claude turn.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"time"

	"github.com/AnkushinDaniil/noti/internal/config"
	"github.com/AnkushinDaniil/noti/internal/telegram"
)

// hookPayload is the JSON structure written to hook stdin by Claude Code.
type hookPayload struct {
	HookEventName string `json:"hook_event_name"`
	Cwd           string `json:"cwd"`
	Message       string `json:"message"`
}

// tgClient is used only in tests to inject a test Telegram client so the
// fallback path can be observed without network I/O.  nil means "construct
// a real client from cfg".
var tgClient *telegram.Client

// Run reads hook JSON from in, builds notification text, and sends it.
// It posts to the broker /notify endpoint first; if the broker is unreachable
// it falls back to a direct Telegram sendMessage.  Always returns nil.
func Run(cfg *config.Config, level string, in io.Reader) error {
	payload, project := parse(in)
	text := buildText(level, project, payload.Message)

	brokerURL := fmt.Sprintf("http://%s:%d", cfg.Broker.Host, cfg.Broker.Port)
	if postBroker(brokerURL, level, project, text) {
		return nil
	}

	// Fallback: direct Telegram send.
	tg := tgClient
	if tg == nil && cfg.Telegram.BotToken != "" && cfg.Telegram.DefaultChatID != "" {
		tg = telegram.New(cfg.Telegram.BotToken)
	}
	if tg != nil {
		chatID := cfg.Telegram.DefaultChatID
		if chatID == "" {
			chatID = "test"
		}
		_, _ = tg.SendMessage(chatID, text, nil)
	}
	return nil
}

// parse decodes the hook JSON from r and returns the payload plus the derived
// project name (basename of cwd, or "").
func parse(r io.Reader) (hookPayload, string) {
	var p hookPayload
	_ = json.NewDecoder(r).Decode(&p)
	project := ""
	if p.Cwd != "" {
		project = filepath.Base(p.Cwd)
	}
	return p, project
}

// buildText constructs the notification text from the level, project, and message.
func buildText(level, project, message string) string {
	switch level {
	case "attention":
		if project != "" && message != "" {
			return fmt.Sprintf("\U0001f514 [%s] %s", project, message)
		}
		if project != "" {
			return fmt.Sprintf("\U0001f514 [%s] Claude needs you", project)
		}
		if message != "" {
			return fmt.Sprintf("\U0001f514 %s", message)
		}
		return "\U0001f514 Claude needs you"
	case "done":
		if project != "" {
			return fmt.Sprintf("✅ [%s] Claude finished", project)
		}
		return "✅ Claude finished"
	default:
		if project != "" && message != "" {
			return fmt.Sprintf("ℹ️ [%s] %s", project, message)
		}
		if message != "" {
			return fmt.Sprintf("ℹ️ %s", message)
		}
		return "ℹ️ Claude notification"
	}
}

// notifyRequest is the JSON body for POST /notify.
type notifyRequest struct {
	Text    string `json:"text"`
	Level   string `json:"level,omitempty"`
	Project string `json:"project,omitempty"`
}

// postBroker attempts to POST text to the broker's /notify endpoint.
// Returns true if the broker accepted the request (HTTP 200).
func postBroker(brokerURL, level, project, text string) bool {
	body, err := json.Marshal(notifyRequest{Text: text, Level: level, Project: project})
	if err != nil {
		return false
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Post(brokerURL+"/notify", "application/json", bytes.NewReader(body))
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}
