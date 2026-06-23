package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/AnkushinDaniil/noti/internal/config"
	"github.com/AnkushinDaniil/noti/internal/telegram"
)

// hookJSON returns an io.Reader containing the JSON hook payload.
func hookJSON(t *testing.T, cwd, message, event string) io.Reader {
	t.Helper()
	b, err := json.Marshal(map[string]string{
		"cwd":             cwd,
		"message":         message,
		"hook_event_name": event,
	})
	if err != nil {
		t.Fatal(err)
	}
	return strings.NewReader(string(b))
}

// makeCfg builds a minimal Config pointing at the given broker address.
func makeCfg(host string, port int, token, chatID string) *config.Config {
	return &config.Config{
		Telegram: config.Telegram{BotToken: token, DefaultChatID: chatID},
		Broker:   config.BrokerCfg{Host: host, Port: port},
	}
}

func TestRun_PostsBroker(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/notify" {
			gotBody, _ = io.ReadAll(r.Body)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(`{"sent":true,"channels":["telegram"]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer srv.Close()

	host, port := splitAddr(t, srv.Listener.Addr().String())
	c := makeCfg(host, port, "", "")

	in := hookJSON(t, "/home/user/myproject", "", "Stop")
	if err := Run(c, "done", in); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}
	if len(gotBody) == 0 {
		t.Fatal("broker /notify was not called")
	}

	var req map[string]string
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("invalid JSON posted to broker: %v", err)
	}
	if !strings.Contains(req["text"], "myproject") {
		t.Errorf("expected project name in text, got %q", req["text"])
	}
	if !strings.Contains(req["text"], "✅") {
		t.Errorf("expected done emoji in text, got %q", req["text"])
	}
}

func TestRun_FallsBackToTelegram(t *testing.T) {
	// Port 19999 has nothing listening — broker POST will time-out quickly.
	c := makeCfg("127.0.0.1", 19999, "faketoken", "12345")

	tc := telegram.NewTest()
	tgClient = tc
	t.Cleanup(func() { tgClient = nil })

	in := hookJSON(t, "/home/user/myproject", "hello", "Notification")
	if err := Run(c, "attention", in); err != nil {
		t.Fatalf("Run returned error: %v", err)
	}

	if len(tc.Outbox) == 0 {
		t.Fatal("expected fallback Telegram send; Outbox is empty")
	}
	sent := tc.Outbox[0]
	if !strings.Contains(sent.Text, "myproject") {
		t.Errorf("expected project in fallback text, got %q", sent.Text)
	}
	if !strings.Contains(sent.Text, "\U0001f514") {
		t.Errorf("expected attention emoji in fallback text, got %q", sent.Text)
	}
}

func TestBuildText(t *testing.T) {
	cases := []struct {
		name    string
		level   string
		payload hookPayload
		wants   []string // substrings that must all appear
	}{
		{"attention with message", "attention",
			hookPayload{Cwd: "/home/user/proj", Message: "Allow Bash?", SessionID: "abcd1234ef"},
			[]string{"🔔", "proj", "needs you", "Allow Bash?", "s:abcd1234"}},
		{"attention no message", "attention",
			hookPayload{Cwd: "/home/user/proj"},
			[]string{"🔔", "proj", "waiting for your input"}},
		{"attention no project", "attention",
			hookPayload{},
			[]string{"🔔", "Claude Code"}},
		{"done no transcript", "done",
			hookPayload{Cwd: "/home/user/proj"},
			[]string{"✅", "proj", "finished", "/home/user/proj"}},
		{"info with message", "info",
			hookPayload{Cwd: "/home/user/proj", Message: "fyi"},
			[]string{"ℹ️", "proj", "fyi"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := buildText(tc.level, tc.payload)
			for _, w := range tc.wants {
				if !strings.Contains(got, w) {
					t.Errorf("buildText(%q) = %q, missing %q", tc.level, got, w)
				}
			}
		})
	}
}

func TestLastAssistantText(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "t.jsonl")
	lines := []string{
		`{"type":"user","message":{"content":"hi"}}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"first"}]}}`,
		`{"type":"system","subtype":"x"}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"second\nline"}]}}`,
	}
	if err := os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := lastAssistantText(p); got != "second line" {
		t.Errorf("lastAssistantText = %q, want %q", got, "second line")
	}
	if lastAssistantText("") != "" || lastAssistantText(filepath.Join(dir, "nope")) != "" {
		t.Error("expected empty result for missing/empty path")
	}
}

// splitAddr splits "host:port" into its parts, failing the test on error.
func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	idx := strings.LastIndex(addr, ":")
	if idx < 0 {
		t.Fatalf("splitAddr: unexpected addr %q", addr)
	}
	port, err := strconv.Atoi(addr[idx+1:])
	if err != nil {
		t.Fatalf("splitAddr: bad port in %q: %v", addr, err)
	}
	return addr[:idx], port
}
