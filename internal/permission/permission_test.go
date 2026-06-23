package permission

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/AnkushinDaniil/noti/internal/config"
)

// stubBroker is an httptest server implementing /ask and /wait for the gate.
type stubBroker struct {
	mu         sync.Mutex
	askCalled  bool
	waitCalled bool
	answer     string // "" => never answered (stays pending)
	answered   bool
}

func newStubBroker(t *testing.T, answer string) (*httptest.Server, *stubBroker) {
	t.Helper()
	sb := &stubBroker{answer: answer}
	mux := http.NewServeMux()
	mux.HandleFunc("/ask", func(w http.ResponseWriter, r *http.Request) {
		sb.mu.Lock()
		sb.askCalled = true
		sb.mu.Unlock()
		writeJSONTest(w, map[string]any{"ticket": "t-1", "status": "pending"})
	})
	mux.HandleFunc("/wait", func(w http.ResponseWriter, r *http.Request) {
		sb.mu.Lock()
		sb.waitCalled = true
		ans := sb.answer
		sb.mu.Unlock()
		if ans == "" {
			writeJSONTest(w, map[string]any{"status": "pending"})
			return
		}
		sb.mu.Lock()
		sb.answered = true
		sb.mu.Unlock()
		writeJSONTest(w, map[string]any{"status": "answered", "answer": ans})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, sb
}

func writeJSONTest(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// runGate runs the gate and returns the trimmed stdout. An empty string means
// pass-through (the gate emitted no permission decision).
func runGate(t *testing.T, cfg *config.Config, brokerURL, payload string) string {
	t.Helper()
	t.Setenv("NOTI_BROKER_URL", brokerURL)
	var out bytes.Buffer
	if err := Run(cfg, strings.NewReader(payload), &out); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	return strings.TrimSpace(out.String())
}

// decisionOf parses a non-empty gate output into its hookSpecificOutput.
func decisionOf(t *testing.T, raw string) hookSpecificOutput {
	t.Helper()
	if raw == "" {
		t.Fatalf("expected a decision but gate emitted nothing")
	}
	var o hookOutput
	if err := json.Unmarshal([]byte(raw), &o); err != nil {
		t.Fatalf("decode gate output %q: %v", raw, err)
	}
	if o.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", o.HookSpecificOutput.HookEventName)
	}
	return o.HookSpecificOutput
}

func enabledCfg() *config.Config {
	return &config.Config{
		Ask: &config.Ask{Permissions: &config.Permissions{Enabled: true, TimeoutSeconds: 30}},
	}
}

func TestGateAllow(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	d := decisionOf(t, runGate(t, enabledCfg(), srv.URL, payload))
	if d.PermissionDecision != decisionAllow {
		t.Errorf("decision = %q, want allow", d.PermissionDecision)
	}
	if !sb.askCalled || !sb.waitCalled {
		t.Errorf("ask=%v wait=%v, want both true", sb.askCalled, sb.waitCalled)
	}
}

func TestGateDeny(t *testing.T) {
	srv, _ := newStubBroker(t, "Deny")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/x"},"cwd":"/tmp/proj","permission_mode":"default"}`
	d := decisionOf(t, runGate(t, enabledCfg(), srv.URL, payload))
	if d.PermissionDecision != decisionDeny {
		t.Errorf("decision = %q, want deny", d.PermissionDecision)
	}
	if d.PermissionDecisionReason != "Denied from phone" {
		t.Errorf("reason = %q, want Denied from phone", d.PermissionDecisionReason)
	}
}

// In a non-default permission mode the tool proceeds without a prompt, so the
// gate MUST pass through (emit NOTHING) and never contact the phone. Emitting a
// permissionDecision here — even "ask" — would force a prompt that auto-mode
// would otherwise have auto-approved. Regression test for that bug.
func TestGateAutoModePassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"acceptEdits"}`
	if raw := runGate(t, enabledCfg(), srv.URL, payload); raw != "" {
		t.Errorf("output = %q, want empty (pass-through) in non-default mode", raw)
	}
	if sb.askCalled || sb.waitCalled {
		t.Errorf("broker contacted in non-default mode (ask=%v wait=%v); want no contact", sb.askCalled, sb.waitCalled)
	}
}

func TestGateTimeoutPassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "") // never answers
	cfg := &config.Config{Ask: &config.Ask{Permissions: &config.Permissions{Enabled: true, TimeoutSeconds: 1}}}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	if raw := runGate(t, cfg, srv.URL, payload); raw != "" {
		t.Errorf("output = %q, want empty (pass-through on timeout)", raw)
	}
	if !sb.askCalled {
		t.Error("ask should have been called")
	}
}

func TestGateDisabledPassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	cfg := &config.Config{Ask: &config.Ask{Permissions: &config.Permissions{Enabled: false, TimeoutSeconds: 30}}}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	if raw := runGate(t, cfg, srv.URL, payload); raw != "" {
		t.Errorf("output = %q, want empty (disabled)", raw)
	}
	if sb.askCalled {
		t.Error("broker /ask must not be called when disabled")
	}
}

func TestGateNonGatedPassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/x"},"cwd":"/tmp/proj","permission_mode":"default"}`
	if raw := runGate(t, enabledCfg(), srv.URL, payload); raw != "" {
		t.Errorf("output = %q, want empty (non-gated)", raw)
	}
	if sb.askCalled {
		t.Error("broker /ask must not be called for a non-gated tool")
	}
}

func TestGateBrokerUnreachablePassThrough(t *testing.T) {
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	if raw := runGate(t, enabledCfg(), "http://127.0.0.1:1", payload); raw != "" {
		t.Errorf("output = %q, want empty (broker unreachable)", raw)
	}
}

func TestGateEmptyStdinPassThrough(t *testing.T) {
	var out bytes.Buffer
	if err := Run(enabledCfg(), strings.NewReader(""), &out); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	if got := strings.TrimSpace(out.String()); got != "" {
		t.Errorf("output = %q, want empty for empty stdin", got)
	}
}

func TestSummarize(t *testing.T) {
	tests := []struct {
		name string
		tool string
		in   string
		want string
	}{
		{"bash command", "Bash", `{"command":"ls -la"}`, "ls -la"},
		{"write file_path", "Write", `{"file_path":"/a/b.go","content":"x"}`, "/a/b.go"},
		{"newlines collapsed", "Bash", `{"command":"a\nb\tc"}`, "a b c"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := summarize(tc.tool, json.RawMessage(tc.in))
			if got != tc.want {
				t.Errorf("summarize = %q, want %q", got, tc.want)
			}
		})
	}
}
