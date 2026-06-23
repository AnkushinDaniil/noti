package notify

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
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
		level, project, message, want string
	}{
		{"attention", "proj", "msg", "\U0001f514 [proj] msg"},
		{"attention", "proj", "", "\U0001f514 [proj] Claude needs you"},
		{"attention", "", "", "\U0001f514 Claude needs you"},
		{"done", "proj", "", "✅ [proj] Claude finished"},
		{"done", "", "", "✅ Claude finished"},
		{"info", "proj", "msg", "ℹ️ [proj] msg"},
		{"info", "", "msg", "ℹ️ msg"},
		{"info", "", "", "ℹ️ Claude notification"},
	}
	for _, tc := range cases {
		got := buildText(tc.level, tc.project, tc.message)
		if got != tc.want {
			t.Errorf("buildText(%q,%q,%q) = %q, want %q",
				tc.level, tc.project, tc.message, got, tc.want)
		}
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
