package broker

import (
	"encoding/json"
	"log"
	"net/http"
	"time"

	"github.com/AnkushinDaniil/noti/internal/version"
)

// handler builds the broker's HTTP mux with panic recovery.
func (b *broker) handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", b.handleHealth)
	mux.HandleFunc("/notify", b.handleNotify)
	mux.HandleFunc("/ask", b.handleAsk)
	mux.HandleFunc("/wait", b.handleWait)
	mux.HandleFunc("/cancel", b.handleCancel)
	mux.HandleFunc("/config", b.handleConfig)
	if b.testMode {
		mux.HandleFunc("/test/inject", b.handleTestInject)
	}
	return recoverMiddleware(notFoundMux(mux))
}

// notFoundMux wraps mux so unmatched routes return a JSON 404.
func notFoundMux(mux *http.ServeMux) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if _, pattern := mux.Handler(r); pattern == "" {
			writeJSON(w, http.StatusNotFound, map[string]any{"error": "not found"})
			return
		}
		mux.ServeHTTP(w, r)
	})
}

// recoverMiddleware recovers from handler panics, logging to stderr and
// returning a 500 JSON error.
func recoverMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				log.Printf("broker: panic handling %s %s: %v", r.Method, r.URL.Path, rec)
				writeJSON(w, http.StatusInternalServerError, map[string]any{"error": "internal error"})
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// writeJSON writes v as a JSON response with the given status code.
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

// decodeBody decodes the JSON request body into v. On failure it writes a 400
// and returns false.
func decodeBody(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"error": "bad request"})
		return false
	}
	return true
}

func (b *broker) handleHealth(w http.ResponseWriter, r *http.Request) {
	connected := b.testMode || b.isConnected()
	writeJSON(w, http.StatusOK, map[string]any{
		"status":             "ok",
		"version":            version.Version,
		"telegram_connected": connected,
		"pending":            b.reg.pendingCount(),
	})
}

func (b *broker) handleNotify(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Text    string `json:"text"`
		Level   string `json:"level"`
		Channel string `json:"channel"`
		ChatID  string `json:"chat_id"`
		Project string `json:"project"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	target := b.cfg.ResolveTarget(req.Project, req.Channel, req.ChatID)
	var channels []string
	sent := false
	if target.ChatID != "" {
		if _, err := b.tg.SendMessage(target.ChatID, req.Text, nil); err != nil {
			log.Printf("broker: notify send failed: %v", err)
		} else {
			sent = true
			channels = append(channels, target.Channel)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": sent, "channels": channels})
}

func (b *broker) handleAsk(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Question string   `json:"question"`
		Options  []string `json:"options"`
		Project  string   `json:"project"`
		ChatID   string   `json:"chat_id"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	target := b.cfg.ResolveTarget(req.Project, "", req.ChatID)
	chatID := target.ChatID
	ask := b.cfg.ResolveAsk(req.Project)

	t := b.reg.create(chatID, req.Options)

	text, markup := buildAskMessage(t.id, req.Question, req.Options)
	if chatID != "" {
		if msgID, err := b.tg.SendMessage(chatID, text, markup); err != nil {
			log.Printf("broker: ask send failed: %v", err)
		} else {
			b.reg.setMessageID(t.id, msgID)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ticket": t.id,
		"status": "pending",
		"ask":    ask,
	})
}

func (b *broker) handleWait(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket  string `json:"ticket"`
		Timeout int    `json:"timeout"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	t := b.reg.get(req.Ticket)
	if t == nil {
		writeJSON(w, http.StatusOK, map[string]any{"status": "unknown_ticket"})
		return
	}
	timeout := req.Timeout
	if timeout <= 0 {
		timeout = 50
	}
	if timeout > maxWaitSeconds {
		timeout = maxWaitSeconds
	}

	timer := time.NewTimer(time.Duration(timeout) * time.Second)
	defer timer.Stop()
	select {
	case <-t.done:
		if answer, ok := t.result(); ok {
			writeJSON(w, http.StatusOK, map[string]any{"status": "answered", "answer": answer})
			return
		}
		// Cancelled.
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	case <-timer.C:
		writeJSON(w, http.StatusOK, map[string]any{"status": "pending"})
	}
}

func (b *broker) handleCancel(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	if t := b.reg.get(req.Ticket); t != nil {
		t.cancel()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (b *broker) handleConfig(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project")
	ask := b.cfg.ResolveAsk(project)
	writeJSON(w, http.StatusOK, map[string]any{
		"ask":                 ask,
		"telegram_configured": b.cfg.Telegram.BotToken != "" && b.cfg.Telegram.DefaultChatID != "",
	})
}

func (b *broker) handleTestInject(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Ticket string `json:"ticket"`
		ChatID string `json:"chat_id"`
		Text   string `json:"text"`
	}
	if !decodeBody(w, r, &req) {
		return
	}
	var t *ticket
	if req.Ticket != "" {
		t = b.reg.get(req.Ticket)
	} else if req.ChatID != "" {
		t = b.reg.pendingForChat(req.ChatID)
	}
	if t == nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false})
		return
	}
	t.resolve(req.Text)
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}
