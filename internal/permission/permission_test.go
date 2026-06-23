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

func runGate(t *testing.T, cfg *config.Config, brokerURL, payload string) hookOutput {
	t.Helper()
	t.Setenv("NOTI_BROKER_URL", brokerURL)
	var out bytes.Buffer
	if err := Run(cfg, strings.NewReader(payload), &out); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	var o hookOutput
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("decode gate output %q: %v", out.String(), err)
	}
	if o.HookSpecificOutput.HookEventName != "PreToolUse" {
		t.Errorf("hookEventName = %q, want PreToolUse", o.HookSpecificOutput.HookEventName)
	}
	return o
}

func enabledCfg() *config.Config {
	return &config.Config{
		Ask: &config.Ask{Permissions: &config.Permissions{Enabled: true, TimeoutSeconds: 30}},
	}
}

func TestGateAllow(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	o := runGate(t, enabledCfg(), srv.URL, payload)
	if o.HookSpecificOutput.PermissionDecision != decisionAllow {
		t.Errorf("decision = %q, want allow", o.HookSpecificOutput.PermissionDecision)
	}
	if !sb.askCalled || !sb.waitCalled {
		t.Errorf("ask=%v wait=%v, want both true", sb.askCalled, sb.waitCalled)
	}
}

func TestGateAutoModePassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	// In a non-default permission mode (auto / acceptEdits / bypassPermissions /
	// dontAsk / plan) the tool proceeds without a prompt, so the gate MUST pass
	// through and never contact the phone. Regression test for phone spam in auto mode.
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"acceptEdits"}`
	o := runGate(t, enabledCfg(), srv.URL, payload)
	if o.HookSpecificOutput.PermissionDecision != decisionAsk {
		t.Errorf("decision = %q, want ask (pass-through) in non-default mode", o.HookSpecificOutput.PermissionDecision)
	}
	if sb.askCalled || sb.waitCalled {
		t.Errorf("broker contacted in non-default mode (ask=%v wait=%v); want no contact", sb.askCalled, sb.waitCalled)
	}
}

func TestGateDeny(t *testing.T) {
	srv, _ := newStubBroker(t, "Deny")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Write","tool_input":{"file_path":"/x"},"cwd":"/tmp/proj","permission_mode":"default"}`
	o := runGate(t, enabledCfg(), srv.URL, payload)
	if o.HookSpecificOutput.PermissionDecision != decisionDeny {
		t.Errorf("decision = %q, want deny", o.HookSpecificOutput.PermissionDecision)
	}
	if o.HookSpecificOutput.PermissionDecisionReason != "Denied from phone" {
		t.Errorf("reason = %q, want Denied from phone", o.HookSpecificOutput.PermissionDecisionReason)
	}
}

func TestGateTimeoutPassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "") // never answers
	// timeout_seconds=1 so the poll loop exits quickly.
	cfg := &config.Config{Ask: &config.Ask{Permissions: &config.Permissions{Enabled: true, TimeoutSeconds: 1}}}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	o := runGate(t, cfg, srv.URL, payload)
	if o.HookSpecificOutput.PermissionDecision != decisionAsk {
		t.Errorf("decision = %q, want ask (pass-through)", o.HookSpecificOutput.PermissionDecision)
	}
	if !sb.askCalled {
		t.Error("ask should have been called")
	}
}

func TestGateDisabledPassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	cfg := &config.Config{Ask: &config.Ask{Permissions: &config.Permissions{Enabled: false, TimeoutSeconds: 30}}}
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	o := runGate(t, cfg, srv.URL, payload)
	if o.HookSpecificOutput.PermissionDecision != decisionAsk {
		t.Errorf("decision = %q, want ask (disabled)", o.HookSpecificOutput.PermissionDecision)
	}
	if sb.askCalled {
		t.Error("broker /ask must not be called when disabled")
	}
}

func TestGateNonGatedPassThrough(t *testing.T) {
	srv, sb := newStubBroker(t, "Allow")
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Read","tool_input":{"file_path":"/x"},"cwd":"/tmp/proj","permission_mode":"default"}`
	o := runGate(t, enabledCfg(), srv.URL, payload)
	if o.HookSpecificOutput.PermissionDecision != decisionAsk {
		t.Errorf("decision = %q, want ask (non-gated)", o.HookSpecificOutput.PermissionDecision)
	}
	if sb.askCalled {
		t.Error("broker /ask must not be called for a non-gated tool")
	}
}

func TestGateBrokerUnreachablePassThrough(t *testing.T) {
	payload := `{"hook_event_name":"PreToolUse","tool_name":"Bash","tool_input":{"command":"ls"},"cwd":"/tmp/proj","permission_mode":"default"}`
	// Point at a closed port.
	o := runGate(t, enabledCfg(), "http://127.0.0.1:1", payload)
	if o.HookSpecificOutput.PermissionDecision != decisionAsk {
		t.Errorf("decision = %q, want ask (broker unreachable)", o.HookSpecificOutput.PermissionDecision)
	}
}

func TestGateEmptyStdinPassThrough(t *testing.T) {
	var out bytes.Buffer
	if err := Run(enabledCfg(), strings.NewReader(""), &out); err != nil {
		t.Fatalf("Run error: %v", err)
	}
	var o hookOutput
	if err := json.Unmarshal(out.Bytes(), &o); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if o.HookSpecificOutput.PermissionDecision != decisionAsk {
		t.Errorf("decision = %q, want ask for empty stdin", o.HookSpecificOutput.PermissionDecision)
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
