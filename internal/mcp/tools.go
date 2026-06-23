package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"
)

// toolDefs returns the 5 tool definitions advertised by tools/list.
func toolDefs() []map[string]any {
	strProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	return []map[string]any{
		{
			"name":        "ask_user",
			"description": "Ask the human a question instead of guessing. The question is shown both on the laptop (an in-editor prompt) and on the phone; whichever device answers first wins and the other prompt is dismissed. Returns the answer, or — if no one replies within the time limit — a ticket to pass to wait_for_reply. Provide options to render answer choices as buttons.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"question": strProp("The question to ask the human."),
					"options": map[string]any{
						"type":        "array",
						"items":       map[string]any{"type": "string"},
						"description": "Optional answer choices presented as buttons.",
					},
				},
				"required": []string{"question"},
			},
		},
		{
			"name":        "wait_for_reply",
			"description": "Keep waiting for the phone reply; call repeatedly.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"ticket": strProp("The ticket id returned by ask_user."),
				},
				"required": []string{"ticket"},
			},
		},
		{
			"name":        "notify",
			"description": "Send a one-way notification to the human's phone.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"text":    strProp("The notification text."),
					"level":   strProp("Optional level (e.g. attention, done, info)."),
					"project": strProp("Optional project name for routing."),
				},
				"required": []string{"text"},
			},
		},
		{
			"name":        "send_file",
			"description": "Send a file to the human's phone as a document.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    strProp("Path to the file to send."),
					"caption": strProp("Optional caption."),
					"project": strProp("Optional project name for routing."),
				},
				"required": []string{"path"},
			},
		},
		{
			"name":        "send_image",
			"description": "Send an image to the human's phone as a photo.",
			"inputSchema": map[string]any{
				"type": "object",
				"properties": map[string]any{
					"path":    strProp("Path to the image to send."),
					"caption": strProp("Optional caption."),
					"project": strProp("Optional project name for routing."),
				},
				"required": []string{"path"},
			},
		},
	}
}

// toolCallParams is the params shape of a tools/call request.
type toolCallParams struct {
	Name      string          `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
}

func (s *server) handleToolsCall(msg rpcRequest) {
	var p toolCallParams
	if len(msg.Params) > 0 {
		if err := json.Unmarshal(msg.Params, &p); err != nil {
			s.replyError(msg.ID, -32602, "invalid params: "+err.Error())
			return
		}
	}

	switch p.Name {
	case "ask_user":
		s.callAskUser(msg.ID, p.Arguments)
	case "wait_for_reply":
		s.callWaitForReply(msg.ID, p.Arguments)
	case "notify":
		s.callNotify(msg.ID, p.Arguments)
	case "send_file":
		s.callSend(msg.ID, p.Arguments, "/send_file")
	case "send_image":
		s.callSend(msg.ID, p.Arguments, "/send_image")
	default:
		s.replyError(msg.ID, -32602, "unknown tool: "+p.Name)
	}
}

// toolResult writes a tools/call success result with a single text content.
func (s *server) toolResult(id json.RawMessage, text string, isError bool) {
	s.reply(id, map[string]any{
		"content": []map[string]any{{"type": "text", "text": text}},
		"isError": isError,
	})
}

const brokerHint = "Could not reach the noti broker. Run `noti broker` or run the /noti:setup skill to configure it."

// ---- ask_user / wait_for_reply ----

type askArgs struct {
	Question string   `json:"question"`
	Options  []string `json:"options"`
	Project  string   `json:"project"`
}

type askResponse struct {
	Ticket string `json:"ticket"`
	Status string `json:"status"`
}

type waitResponse struct {
	Status string `json:"status"`
	Answer string `json:"answer"`
}

// askConfig is the subset of broker /config we consume: the resolved Ask block.
type askConfig struct {
	Ask resolvedAsk `json:"ask"`
}

// resolvedAsk mirrors config.Ask's JSON. Pointer fields distinguish unset from
// an explicit value; nil defaults to true.
type resolvedAsk struct {
	Mode               string `json:"mode"`
	IdleTimeoutSeconds int    `json:"idle_timeout_seconds"`
	Laptop             *bool  `json:"laptop"`
	RequireLaptop      *bool  `json:"require_laptop"`
}

// elicitResult is the routed elicitation/create response payload.
type elicitResult struct {
	Action  string                     `json:"action"`
	Content map[string]json.RawMessage `json:"content"`
}

// askOutcome carries the winning device and its answer through the race.
type askOutcome struct {
	winner string
	answer string
}

const (
	defaultAskCeiling = 50 * time.Second
	minIdleSecs       = 1
	maxIdleSecs       = 50
	phonePollSec      = 5
)

// elicitationSchema builds the elicitation/create requestedSchema for the given
// answer options. With options it constrains a "choice" enum; without options
// it requests free-text "reply".
func elicitationSchema(options []string) map[string]any {
	if len(options) > 0 {
		return map[string]any{
			"type": "object",
			"properties": map[string]any{
				"choice": map[string]any{
					"type":      "string",
					"enum":      options,
					"enumNames": options,
				},
			},
			"required": []string{"choice"},
		}
	}
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"reply": map[string]any{
				"type":  "string",
				"title": "Your reply",
			},
		},
		"required": []string{"reply"},
	}
}

// elicitAnswer extracts the accepted answer string from a routed elicitation
// response. It returns (answer, true) only when action == "accept" and a
// "choice" or "reply" string is present; decline/cancel yield ("", false).
func elicitAnswer(res elicitResult) (string, bool) {
	if res.Action != "accept" {
		return "", false
	}
	for _, key := range []string{"choice", "reply"} {
		if raw, ok := res.Content[key]; ok {
			var v string
			if err := json.Unmarshal(raw, &v); err == nil {
				return v, true
			}
		}
	}
	return "", false
}

// callAskUser mirrors the question to the laptop (MCP elicitation) and the
// phone (broker), per the configured mode, and returns the first answer; the
// losing prompt is cancelled best-effort. After a ~50s ceiling with no winner,
// the laptop prompt is dismissed and the phone ticket is handed back for
// wait_for_reply.
func (s *server) callAskUser(id json.RawMessage, rawArgs json.RawMessage) {
	var a askArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			s.toolResult(id, "invalid arguments: "+err.Error(), true)
			return
		}
	}
	if a.Question == "" {
		s.toolResult(id, "ask_user requires a non-empty question.", true)
		return
	}

	// Resolve the mode and gating from the broker config.
	var cfg askConfig
	if err := s.brokerGet("/config?project="+url.QueryEscape(a.Project), &cfg); err != nil {
		s.toolResult(id, brokerHint, true)
		return
	}
	ask := cfg.Ask

	laptopWanted := ask.Laptop == nil || *ask.Laptop
	if laptopWanted && !s.clientElicitation {
		requireLaptop := ask.RequireLaptop == nil || *ask.RequireLaptop
		if requireLaptop {
			s.toolResult(id, "noti needs Claude Code with MCP elicitation (v2.1.76+). Update Claude Code, or set ask.require_laptop=false for phone-only.", true)
			return
		}
		laptopWanted = false // phone-only fallback
	}

	ctx, cancelAll := context.WithCancel(context.Background())
	defer cancelAll()

	var once sync.Once
	resultCh := make(chan askOutcome, 1)
	claim := func(winner, answer string) {
		once.Do(func() { resultCh <- askOutcome{winner: winner, answer: answer} })
	}

	var elicCancel func() // set if the laptop prompt was started

	var phoneMu sync.Mutex
	var phoneTicket string
	var phoneStarted bool

	startPhone := func() {
		phoneMu.Lock()
		if phoneStarted {
			phoneMu.Unlock()
			return
		}
		phoneStarted = true
		phoneMu.Unlock()

		go func() {
			var resp askResponse
			if err := s.brokerPost("/ask", map[string]any{
				"question": a.Question,
				"options":  a.Options,
				"project":  a.Project,
			}, &resp); err != nil {
				return
			}
			phoneMu.Lock()
			phoneTicket = resp.Ticket
			phoneMu.Unlock()

			for ctx.Err() == nil {
				var w waitResponse
				if err := s.brokerPost("/wait", map[string]any{
					"ticket":  resp.Ticket,
					"timeout": phonePollSec,
				}, &w); err != nil {
					return
				}
				if w.Status == "answered" {
					claim("phone", w.Answer)
					return
				}
				if ctx.Err() != nil {
					return
				}
			}
		}()
	}

	startLaptop := func() {
		if !laptopWanted {
			return
		}
		ch, cancel := s.sendServerRequest("elicitation/create", map[string]any{
			"message":         a.Question,
			"requestedSchema": elicitationSchema(a.Options),
		})
		elicCancel = cancel
		go func() {
			select {
			case msg := <-ch:
				var res elicitResult
				if len(msg.Result) > 0 {
					_ = json.Unmarshal(msg.Result, &res)
				}
				if answer, ok := elicitAnswer(res); ok {
					claim("laptop", answer)
					return
				}
				// decline/cancel: no laptop answer. In timeout mode this is the
				// signal to escalate to the phone immediately.
				if ask.Mode != "forward-all" {
					startPhone()
				}
			case <-ctx.Done():
			}
		}()
	}

	startLaptop()
	switch {
	case !laptopWanted:
		// No laptop prompt: go straight to the phone regardless of mode.
		startPhone()
	case ask.Mode == "forward-all":
		startPhone()
	default: // "timeout": laptop first, escalate to phone after idle.
		idle := clampIdle(ask.IdleTimeoutSeconds)
		t := time.AfterFunc(idle, func() {
			select {
			case <-ctx.Done():
			default:
				startPhone()
			}
		})
		defer t.Stop()
	}

	ceilingDur := s.askCeiling
	if ceilingDur <= 0 {
		ceilingDur = defaultAskCeiling
	}
	ceiling := time.NewTimer(ceilingDur)
	defer ceiling.Stop()

	select {
	case r := <-resultCh:
		cancelAll()
		switch r.winner {
		case "laptop":
			phoneMu.Lock()
			tk := phoneTicket
			phoneMu.Unlock()
			if tk != "" {
				_ = s.brokerPost("/cancel", map[string]any{"ticket": tk}, nil)
			}
		case "phone":
			if elicCancel != nil {
				elicCancel() // dismiss the laptop prompt best-effort + drop late
			}
		}
		s.toolResult(id, r.answer, false)
	case <-ceiling.C:
		if elicCancel != nil {
			elicCancel() // stop the laptop prompt; continue phone-only
		}
		phoneMu.Lock()
		tk := phoneTicket
		phoneMu.Unlock()
		if tk != "" {
			s.toolResult(id, fmt.Sprintf("No reply yet. Call wait_for_reply with ticket=%q to keep waiting.", tk), false)
		} else {
			s.toolResult(id, "No reply within the time limit.", false)
		}
	}
}

// clampIdle clamps an idle-timeout in seconds to [minIdleSecs, maxIdleSecs] and
// returns the corresponding duration.
func clampIdle(secs int) time.Duration {
	if secs < minIdleSecs {
		secs = minIdleSecs
	}
	if secs > maxIdleSecs {
		secs = maxIdleSecs
	}
	return time.Duration(secs) * time.Second
}

type waitArgs struct {
	Ticket string `json:"ticket"`
}

func (s *server) callWaitForReply(id json.RawMessage, rawArgs json.RawMessage) {
	var a waitArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			s.toolResult(id, "invalid arguments: "+err.Error(), true)
			return
		}
	}
	if a.Ticket == "" {
		s.toolResult(id, "wait_for_reply requires a ticket.", true)
		return
	}

	var wait waitResponse
	if err := s.brokerPost("/wait", map[string]any{
		"ticket":  a.Ticket,
		"timeout": 50,
	}, &wait); err != nil {
		s.toolResult(id, brokerHint, true)
		return
	}

	switch wait.Status {
	case "answered":
		s.toolResult(id, wait.Answer, false)
	case "pending":
		s.toolResult(id, "Still waiting, call wait_for_reply again.", false)
	case "unknown_ticket":
		s.toolResult(id, fmt.Sprintf("Unknown ticket %q.", a.Ticket), true)
	default:
		s.toolResult(id, "Unexpected wait status: "+wait.Status, true)
	}
}

// ---- notify ----

type notifyArgs struct {
	Text    string `json:"text"`
	Level   string `json:"level"`
	Project string `json:"project"`
}

func (s *server) callNotify(id json.RawMessage, rawArgs json.RawMessage) {
	var a notifyArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			s.toolResult(id, "invalid arguments: "+err.Error(), true)
			return
		}
	}
	if a.Text == "" {
		s.toolResult(id, "notify requires non-empty text.", true)
		return
	}
	var resp json.RawMessage
	if err := s.brokerPost("/notify", map[string]any{
		"text":    a.Text,
		"level":   a.Level,
		"project": a.Project,
	}, &resp); err != nil {
		s.toolResult(id, brokerHint, true)
		return
	}
	s.toolResult(id, "Notification sent.", false)
}

// ---- send_file / send_image ----

type sendArgs struct {
	Path    string `json:"path"`
	Caption string `json:"caption"`
	Project string `json:"project"`
}

func (s *server) callSend(id json.RawMessage, rawArgs json.RawMessage, endpoint string) {
	var a sendArgs
	if len(rawArgs) > 0 {
		if err := json.Unmarshal(rawArgs, &a); err != nil {
			s.toolResult(id, "invalid arguments: "+err.Error(), true)
			return
		}
	}
	if a.Path == "" {
		s.toolResult(id, "a non-empty path is required.", true)
		return
	}
	var resp json.RawMessage
	if err := s.brokerPost(endpoint, map[string]any{
		"path":    a.Path,
		"caption": a.Caption,
		"project": a.Project,
	}, &resp); err != nil {
		s.toolResult(id, brokerHint, true)
		return
	}
	s.toolResult(id, "Sent "+a.Path+".", false)
}

// brokerGet GETs the broker path and decodes the JSON response into out (which
// may be nil to discard). It returns an error if the broker is unreachable or
// returns a non-2xx status.
func (s *server) brokerGet(path string, out any) error {
	req, err := http.NewRequest(http.MethodGet, s.brokerURL+path, nil)
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	resp, err := s.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("broker request to %s: %w", path, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read broker response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("broker %s returned status %d: %s", path, resp.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode broker response: %w", err)
		}
	}
	return nil
}

// brokerPost POSTs body as JSON to the broker endpoint and decodes the JSON
// response into out (which may be nil to discard). It returns an error if the
// broker is unreachable or returns a non-2xx status.
func (s *server) brokerPost(endpoint string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	req, err := http.NewRequest(http.MethodPost, s.brokerURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := s.httpc.Do(req)
	if err != nil {
		return fmt.Errorf("broker request to %s: %w", endpoint, err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("read broker response: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("broker %s returned status %d: %s", endpoint, resp.StatusCode, string(respBody))
	}
	if out != nil {
		if err := json.Unmarshal(respBody, out); err != nil {
			return fmt.Errorf("decode broker response: %w", err)
		}
	}
	return nil
}
