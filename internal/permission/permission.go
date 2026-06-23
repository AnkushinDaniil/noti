// Package permission implements the noti permission-gate invoked by
// `noti permission-gate`. It is wired to Claude Code's PreToolUse hook: it
// reads the hook JSON payload from stdin, and — if the gate is enabled, the
// tool is in the gated set, and the broker is reachable — asks the human on
// their phone (broker /ask) whether to allow the tool, polling /wait up to a
// configurable timeout. It emits a JSON-RPC-style permission decision on
// stdout and ALWAYS exits 0: any failure, timeout, or non-gated tool yields a
// pass-through decision so the hook never wedges a Claude turn.
//
// The verified PreToolUse hook contract (code.claude.com/docs/en/hooks):
//
//	stdin:  {"hook_event_name":"PreToolUse","tool_name":"Bash",
//	         "tool_input":{...},"cwd":"...","session_id":"...",
//	         "transcript_path":"...","permission_mode":"..."}
//	stdout: {"hookSpecificOutput":{"hookEventName":"PreToolUse",
//	         "permissionDecision":"allow"|"deny"|"ask",
//	         "permissionDecisionReason":"..."}}
//
// "ask" defers to the normal terminal permission prompt (pass-through).
package permission

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strings"
	"time"

	"github.com/AnkushinDaniil/noti/internal/config"
)

const (
	hookEventName = "PreToolUse"

	decisionAllow = "allow"
	decisionDeny  = "deny"
	decisionAsk   = "ask" // pass-through to the normal terminal prompt

	// brokerAskTimeout bounds a single broker HTTP call.
	brokerAskTimeout = 5 * time.Second
	// waitPollSeconds is the per-/wait poll window the broker holds the request.
	waitPollSeconds = 5
)

// hookInput is the subset of the PreToolUse hook stdin payload we use.
type hookInput struct {
	HookEventName string          `json:"hook_event_name"`
	ToolName      string          `json:"tool_name"`
	ToolInput     json.RawMessage `json:"tool_input"`
	Cwd           string          `json:"cwd"`
}

// hookSpecificOutput mirrors Claude Code's PreToolUse decision object.
type hookSpecificOutput struct {
	HookEventName            string `json:"hookEventName"`
	PermissionDecision       string `json:"permissionDecision"`
	PermissionDecisionReason string `json:"permissionDecisionReason,omitempty"`
}

type hookOutput struct {
	HookSpecificOutput hookSpecificOutput `json:"hookSpecificOutput"`
}

type askResponse struct {
	Ticket string `json:"ticket"`
	Status string `json:"status"`
}

type waitResponse struct {
	Status string `json:"status"`
	Answer string `json:"answer"`
}

// Run reads the PreToolUse hook payload from in, decides allow/deny/ask, and
// writes the decision JSON to out. It always returns nil: the gate must never
// break a Claude turn. The caller (main) exits 0 regardless.
func Run(cfg *config.Config, in io.Reader, out io.Writer) error {
	input, err := parse(in)
	if err != nil {
		// Unparseable stdin: pass through, never block.
		writeDecision(out, decisionAsk, "")
		return nil
	}

	project := projectName(input.Cwd)
	ask := cfg.ResolveAsk(project)
	perms := ask.Permissions

	// Disabled, no config, or tool not gated → pass through without the broker.
	if perms == nil || !perms.Enabled || !gated(perms.Tools, input.ToolName) {
		writeDecision(out, decisionAsk, "")
		return nil
	}

	brokerURL := brokerBaseURL(cfg)
	timeout := perms.TimeoutSeconds
	if timeout <= 0 {
		timeout = 30
	}

	decision, reason := gate(brokerURL, input, project, timeout)
	writeDecision(out, decision, reason)
	return nil
}

// gate performs the phone-first permission flow: POST /ask, then poll /wait up
// to timeout seconds. Returns the decision and an optional reason. Any broker
// failure, timeout, or unrecognized answer yields a pass-through ("ask").
func gate(brokerURL string, input hookInput, project string, timeout int) (string, string) {
	question := fmt.Sprintf("Allow %s? %s", input.ToolName, summarize(input.ToolName, input.ToolInput))

	var ask askResponse
	if err := brokerPost(brokerURL, "/ask", map[string]any{
		"question": question,
		"options":  []string{"Allow", "Deny"},
		"project":  project,
	}, &ask); err != nil || ask.Ticket == "" {
		return decisionAsk, ""
	}

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for time.Now().Before(deadline) {
		var w waitResponse
		if err := brokerPost(brokerURL, "/wait", map[string]any{
			"ticket":  ask.Ticket,
			"timeout": waitPollSeconds,
		}, &w); err != nil {
			// Broker became unreachable mid-poll: pass through.
			return decisionAsk, ""
		}
		if w.Status == "answered" {
			switch strings.TrimSpace(w.Answer) {
			case "Allow":
				return decisionAllow, "Allowed from phone"
			case "Deny":
				return decisionDeny, "Denied from phone"
			default:
				return decisionAsk, ""
			}
		}
		if w.Status == "unknown_ticket" {
			return decisionAsk, ""
		}
		// "pending": keep polling until the deadline.
	}
	// Timed out waiting for a phone answer: defer to the terminal prompt.
	return decisionAsk, ""
}

// parse decodes the hook JSON from r.
func parse(r io.Reader) (hookInput, error) {
	data, err := io.ReadAll(r)
	if err != nil {
		return hookInput{}, fmt.Errorf("read hook stdin: %w", err)
	}
	var in hookInput
	if len(bytes.TrimSpace(data)) == 0 {
		return hookInput{}, fmt.Errorf("empty hook stdin")
	}
	if err := json.Unmarshal(data, &in); err != nil {
		return hookInput{}, fmt.Errorf("decode hook payload: %w", err)
	}
	return in, nil
}

// gated reports whether toolName is in the configured gated tool set.
func gated(tools []string, toolName string) bool {
	for _, t := range tools {
		if t == toolName {
			return true
		}
	}
	return false
}

// projectName derives the routing key (basename of cwd) from the hook cwd.
func projectName(cwd string) string {
	if cwd == "" {
		return ""
	}
	return filepath.Base(cwd)
}

// summarize produces a short, single-line description of the tool input for
// the phone prompt. It best-effort extracts the most relevant field per tool.
func summarize(toolName string, raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	var key string
	switch toolName {
	case "Bash":
		key = "command"
	case "Write", "Edit", "NotebookEdit":
		key = "file_path"
	}
	if key != "" {
		if v, ok := m[key].(string); ok && v != "" {
			return truncate(oneLine(v), 200)
		}
	}
	// Fallback: compact JSON of the input.
	if b, err := json.Marshal(m); err == nil {
		return truncate(string(b), 200)
	}
	return ""
}

// oneLine collapses newlines and tabs into single spaces.
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

// brokerBaseURL resolves the broker base URL, honoring NOTI_BROKER_URL for
// test/override flexibility and otherwise building it from the config.
func brokerBaseURL(cfg *config.Config) string {
	if u := brokerEnvURL(); u != "" {
		return u
	}
	return fmt.Sprintf("http://%s:%d", cfg.Broker.Host, cfg.Broker.Port)
}

// writeDecision emits the PreToolUse decision JSON to out (always valid JSON).
func writeDecision(out io.Writer, decision, reason string) {
	o := hookOutput{HookSpecificOutput: hookSpecificOutput{
		HookEventName:            hookEventName,
		PermissionDecision:       decision,
		PermissionDecisionReason: reason,
	}}
	data, err := json.Marshal(o)
	if err != nil {
		return
	}
	_, _ = out.Write(append(data, '\n'))
}

// brokerPost POSTs body as JSON to brokerURL+endpoint with a short timeout and
// decodes the JSON response into out (nil to discard). Returns an error if the
// broker is unreachable or returns a non-2xx status.
func brokerPost(brokerURL, endpoint string, body any, out any) error {
	data, err := json.Marshal(body)
	if err != nil {
		return fmt.Errorf("marshal request: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), brokerAskTimeout+time.Duration(waitPollSeconds)*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, brokerURL+endpoint, bytes.NewReader(data))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	client := &http.Client{Timeout: brokerAskTimeout + time.Duration(waitPollSeconds)*time.Second}
	resp, err := client.Do(req)
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
