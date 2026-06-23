// Package notify implements the hook notifier invoked by `noti notify <level>`.
// It reads a Claude hook JSON payload from an io.Reader, builds a human-friendly
// notification that makes clear WHICH session/project it came from and WHAT is
// happening, and delivers it via the broker (POST /notify, 5-second timeout)
// with a direct Telegram fallback when the broker is unreachable. Run always
// returns nil — the hook must never break a Claude turn.
package notify

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/AnkushinDaniil/noti/internal/config"
	"github.com/AnkushinDaniil/noti/internal/telegram"
)

// hookPayload is the subset of the Claude hook stdin JSON that we use.
type hookPayload struct {
	HookEventName  string `json:"hook_event_name"`
	Cwd            string `json:"cwd"`
	Message        string `json:"message"`
	SessionID      string `json:"session_id"`
	TranscriptPath string `json:"transcript_path"`
}

// tgClient is used only in tests to inject a test Telegram client so the
// fallback path can be observed without network I/O. nil means "construct a
// real client from cfg".
var tgClient *telegram.Client

// Run reads hook JSON from in, builds notification text, and sends it. It posts
// to the broker /notify endpoint first; if the broker is unreachable it falls
// back to a direct Telegram sendMessage. Always returns nil.
func Run(cfg *config.Config, level string, in io.Reader) error {
	p := parse(in)
	text := buildText(level, p)

	brokerURL := fmt.Sprintf("http://%s:%d", cfg.Broker.Host, cfg.Broker.Port)
	if postBroker(brokerURL, level, projectName(p.Cwd), text) {
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

// parse decodes the hook JSON from r.
func parse(r io.Reader) hookPayload {
	var p hookPayload
	_ = json.NewDecoder(r).Decode(&p)
	return p
}

// projectName is the basename of cwd, or "".
func projectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Base(cwd)
}

// buildText composes a 3-part notification:
//
//	<emoji> <project> <state>
//	📂 <cwd>  ·  s:<short session id>
//	— <context: what Claude is asking / last said>
//
// so the user can tell which session it came from and what is happening.
func buildText(level string, p hookPayload) string {
	project := projectName(p.Cwd)
	if project == "" {
		project = "Claude Code"
	}

	var head, context string
	switch level {
	case "attention":
		head = "🔔 " + project + " — needs you"
		context = oneLine(p.Message)
		if context == "" {
			context = "Claude is waiting for your input."
		}
	case "done":
		head = "✅ " + project + " — finished"
		context = lastAssistantText(p.TranscriptPath)
	default:
		head = "ℹ️ " + project
		context = oneLine(p.Message)
	}

	var b strings.Builder
	b.WriteString(head)
	if loc := location(p); loc != "" {
		b.WriteString("\n📂 " + loc)
	}
	if context != "" {
		b.WriteString("\n— " + truncate(context, 350))
	}
	return b.String()
}

// location renders "<cwd>  ·  s:<short session>" to disambiguate which
// project/session produced the notification.
func location(p hookPayload) string {
	var parts []string
	if p.Cwd != "" {
		parts = append(parts, abbreviateHome(p.Cwd))
	}
	if s := shortSession(p.SessionID); s != "" {
		parts = append(parts, "s:"+s)
	}
	return strings.Join(parts, "  ·  ")
}

// abbreviateHome replaces a leading $HOME with "~".
func abbreviateHome(path string) string {
	if home, err := os.UserHomeDir(); err == nil && home != "" && strings.HasPrefix(path, home) {
		return "~" + path[len(home):]
	}
	return path
}

// shortSession returns the first 8 chars of the session id (the transcript file
// prefix), for compact display.
func shortSession(id string) string {
	id = strings.TrimSpace(id)
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

// lastAssistantText extracts the final assistant text block from the transcript
// JSONL, reading only the tail of the file. Best-effort: returns "" on any error.
func lastAssistantText(path string) string {
	if path == "" {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return ""
	}
	const tail = 256 * 1024
	var start int64
	if fi.Size() > tail {
		start = fi.Size() - tail
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return ""
	}
	data, err := io.ReadAll(f)
	if err != nil {
		return ""
	}
	lines := bytes.Split(data, []byte("\n"))
	if start > 0 && len(lines) > 0 {
		lines = lines[1:] // drop the partial first line after seeking
	}
	last := ""
	for _, ln := range lines {
		ln = bytes.TrimSpace(ln)
		if len(ln) == 0 {
			continue
		}
		var o struct {
			Type    string `json:"type"`
			Message struct {
				Content json.RawMessage `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(ln, &o) != nil {
			continue
		}
		if o.Type != "assistant" {
			continue
		}
		if t := contentText(o.Message.Content); t != "" {
			last = t
		}
	}
	return oneLine(last)
}

// contentText extracts concatenated text from an Anthropic message content,
// which may be a plain string or an array of typed blocks.
func contentText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, blk := range blocks {
			if blk.Type == "text" && blk.Text != "" {
				if b.Len() > 0 {
					b.WriteString(" ")
				}
				b.WriteString(blk.Text)
			}
		}
		return b.String()
	}
	return ""
}

// oneLine collapses newlines/tabs into single spaces and trims.
func oneLine(s string) string {
	r := strings.NewReplacer("\n", " ", "\r", " ", "\t", " ")
	return strings.TrimSpace(r.Replace(s))
}

// truncate shortens s to at most n runes, appending an ellipsis if cut.
func truncate(s string, n int) string {
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return string(runes[:n]) + "…"
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
