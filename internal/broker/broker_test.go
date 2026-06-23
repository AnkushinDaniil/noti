package broker

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/AnkushinDaniil/noti/internal/config"
	"github.com/AnkushinDaniil/noti/internal/telegram"
	"github.com/AnkushinDaniil/noti/internal/version"
)

// startTestBroker spins the broker on an ephemeral loopback port in test mode
// with the given config and a temp DataDir. It returns the base URL, the
// broker (for Outbox inspection), and a cleanup function.
func startTestBroker(t *testing.T, cfg *config.Config) (string, *broker, func()) {
	t.Helper()
	t.Setenv("NOTI_TEST", "1")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	dataDir := config.DataDir()
	lock, err := acquireLock(filepath.Join(dataDir, "broker.lock"))
	if err != nil {
		t.Fatalf("acquireLock: %v", err)
	}

	b := &broker{
		cfg:      cfg,
		tg:       telegram.NewTest(),
		reg:      newRegistry(),
		dataDir:  dataDir,
		testMode: true,
	}

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	srv := &http.Server{Handler: b.handler()}
	stop := make(chan struct{})
	go b.runReaper(stop)
	go func() { _ = srv.Serve(ln) }()

	base := "http://" + ln.Addr().String()
	cleanup := func() {
		_ = srv.Close()
		close(stop)
		lock.release()
	}
	return base, b, cleanup
}

func testConfig() *config.Config {
	c := &config.Config{
		Telegram: config.Telegram{BotToken: "TESTTOKEN", DefaultChatID: "12345"},
	}
	// apply broker defaults via Load semantics
	c.Broker.Host = "127.0.0.1"
	c.Broker.Port = 0
	return c
}

func postJSON(t *testing.T, url string, body any) map[string]any {
	t.Helper()
	data, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(data))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode %s: %v", url, err)
	}
	return out
}

func TestHealth(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	resp, err := http.Get(base + "/health")
	if err != nil {
		t.Fatalf("GET /health: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["status"] != "ok" {
		t.Errorf("status = %v, want ok", out["status"])
	}
	if out["version"] != version.Version {
		t.Errorf("version = %v, want %v", out["version"], version.Version)
	}
	if out["telegram_connected"] != true {
		t.Errorf("telegram_connected = %v, want true (test mode)", out["telegram_connected"])
	}
	if out["pending"].(float64) != 0 {
		t.Errorf("pending = %v, want 0", out["pending"])
	}
}

func TestAskReturnsPendingTicket(t *testing.T) {
	base, b, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	out := postJSON(t, base+"/ask", map[string]any{"question": "Proceed?"})
	if out["status"] != "pending" {
		t.Errorf("status = %v, want pending", out["status"])
	}
	if out["ticket"] == "" || out["ticket"] == nil {
		t.Errorf("ticket missing: %v", out)
	}
	if out["ask"] == nil {
		t.Errorf("ask block missing")
	}
	// Telegram message must have been sent (recorded in Outbox).
	if len(b.tg.Outbox) != 1 {
		t.Fatalf("Outbox len = %d, want 1", len(b.tg.Outbox))
	}
	if b.tg.Outbox[0].Method != "sendMessage" {
		t.Errorf("method = %s, want sendMessage", b.tg.Outbox[0].Method)
	}
}

func TestWaitResolvedByInject(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	ask := postJSON(t, base+"/ask", map[string]any{"question": "Color?"})
	ticket := ask["ticket"].(string)

	var wg sync.WaitGroup
	var waitResult map[string]any
	wg.Add(1)
	go func() {
		defer wg.Done()
		waitResult = postJSON(t, base+"/wait", map[string]any{"ticket": ticket, "timeout": 5})
	}()

	// Give the wait a moment to block, then inject.
	time.Sleep(50 * time.Millisecond)
	inj := postJSON(t, base+"/test/inject", map[string]any{"ticket": ticket, "text": "blue"})
	if inj["ok"] != true {
		t.Errorf("inject ok = %v, want true", inj["ok"])
	}

	wg.Wait()
	if waitResult["status"] != "answered" {
		t.Errorf("status = %v, want answered", waitResult["status"])
	}
	if waitResult["answer"] != "blue" {
		t.Errorf("answer = %v, want blue", waitResult["answer"])
	}
}

func TestWaitTimeoutPending(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	ask := postJSON(t, base+"/ask", map[string]any{"question": "Q?"})
	ticket := ask["ticket"].(string)

	out := postJSON(t, base+"/wait", map[string]any{"ticket": ticket, "timeout": 1})
	if out["status"] != "pending" {
		t.Errorf("status = %v, want pending", out["status"])
	}
}

func TestWaitUnknownTicket(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	out := postJSON(t, base+"/wait", map[string]any{"ticket": "nope", "timeout": 1})
	if out["status"] != "unknown_ticket" {
		t.Errorf("status = %v, want unknown_ticket", out["status"])
	}
}

func TestInjectByChatFallback(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	ask := postJSON(t, base+"/ask", map[string]any{"question": "Q?"})
	ticket := ask["ticket"].(string)

	// Inject by chat id (single pending ticket fallback).
	inj := postJSON(t, base+"/test/inject", map[string]any{"chat_id": "12345", "text": "ok"})
	if inj["ok"] != true {
		t.Fatalf("inject ok = %v", inj["ok"])
	}
	out := postJSON(t, base+"/wait", map[string]any{"ticket": ticket, "timeout": 2})
	if out["answer"] != "ok" {
		t.Errorf("answer = %v, want ok", out["answer"])
	}
}

func TestNotify(t *testing.T) {
	base, b, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	out := postJSON(t, base+"/notify", map[string]any{"text": "hello"})
	if out["sent"] != true {
		t.Errorf("sent = %v, want true", out["sent"])
	}
	chans, _ := out["channels"].([]any)
	if len(chans) != 1 || chans[0] != "telegram" {
		t.Errorf("channels = %v, want [telegram]", out["channels"])
	}
	if len(b.tg.Outbox) != 1 || b.tg.Outbox[0].Text != "hello" {
		t.Errorf("Outbox = %+v", b.tg.Outbox)
	}
}

func TestCancelIdempotent(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	ask := postJSON(t, base+"/ask", map[string]any{"question": "Q?"})
	ticket := ask["ticket"].(string)

	for i := 0; i < 2; i++ {
		out := postJSON(t, base+"/cancel", map[string]any{"ticket": ticket})
		if out["ok"] != true {
			t.Errorf("cancel #%d ok = %v, want true", i, out["ok"])
		}
	}
	// A cancelled ticket waits and returns pending (not answered).
	out := postJSON(t, base+"/wait", map[string]any{"ticket": ticket, "timeout": 1})
	if out["status"] != "pending" {
		t.Errorf("status after cancel = %v, want pending", out["status"])
	}
}

func TestConfigEndpoint(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	resp, err := http.Get(base + "/config?project=myproj")
	if err != nil {
		t.Fatalf("GET /config: %v", err)
	}
	defer resp.Body.Close()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["telegram_configured"] != true {
		t.Errorf("telegram_configured = %v, want true", out["telegram_configured"])
	}
	ask, ok := out["ask"].(map[string]any)
	if !ok {
		t.Fatalf("ask block missing or wrong type: %v", out["ask"])
	}
	if ask["mode"] != "timeout" {
		t.Errorf("ask.mode = %v, want timeout", ask["mode"])
	}
	if ask["idle_timeout_seconds"].(float64) != 30 {
		t.Errorf("ask.idle_timeout_seconds = %v, want 30", ask["idle_timeout_seconds"])
	}
}

func TestRegistryFirstWins(t *testing.T) {
	r := newRegistry()
	tk := r.create("c1", nil)
	if !tk.resolve("first") {
		t.Fatal("first resolve should win")
	}
	if tk.resolve("second") {
		t.Fatal("second resolve should be a no-op")
	}
	answer, ok := tk.result()
	if !ok || answer != "first" {
		t.Errorf("result = (%q, %v), want (first, true)", answer, ok)
	}
}

func TestNotFound(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	resp, err := http.Get(base + "/nope")
	if err != nil {
		t.Fatalf("GET /nope: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("status = %d, want 404", resp.StatusCode)
	}
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	if out["error"] != "not found" {
		t.Errorf("error = %v, want 'not found'", out["error"])
	}
}

func TestBadJSON(t *testing.T) {
	base, _, cleanup := startTestBroker(t, testConfig())
	defer cleanup()

	resp, err := http.Post(base+"/notify", "application/json", bytes.NewReader([]byte("{not json")))
	if err != nil {
		t.Fatalf("POST /notify: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
}

func TestParseCallbackData(t *testing.T) {
	tests := []struct {
		data   string
		ticket string
		idx    int
		ok     bool
	}{
		{"noti:t123-1:0", "t123-1", 0, true},
		{"noti:abc:5", "abc", 5, true},
		{"bad:data", "", 0, false},
		{"noti:onlyone", "", 0, false},
		{"noti:t:x", "", 0, false},
	}
	for _, tt := range tests {
		ticket, idx, ok := parseCallbackData(tt.data)
		if ok != tt.ok || ticket != tt.ticket || idx != tt.idx {
			t.Errorf("parseCallbackData(%q) = (%q,%d,%v), want (%q,%d,%v)",
				tt.data, ticket, idx, ok, tt.ticket, tt.idx, tt.ok)
		}
	}
}

func TestHandleCallbackResolves(t *testing.T) {
	cfg := testConfig()
	_, b, cleanup := startTestBroker(t, cfg)
	defer cleanup()

	tk := b.reg.create("12345", []string{"Yes", "No"})
	allow := b.allowSet()
	cb := &telegram.CallbackQuery{
		ID:   "cb1",
		Data: fmt.Sprintf("noti:%s:1", tk.id),
		Message: &telegram.Message{
			MessageID: 99,
			Chat:      telegram.Chat{ID: 12345},
		},
	}
	b.handleCallback(cb, allow)
	answer, ok := tk.result()
	if !ok || answer != "No" {
		t.Errorf("result = (%q,%v), want (No,true)", answer, ok)
	}
}

func TestHandleReplyByMessageID(t *testing.T) {
	cfg := testConfig()
	_, b, cleanup := startTestBroker(t, cfg)
	defer cleanup()

	tk := b.reg.create("12345", nil)
	b.reg.setMessageID(tk.id, 42)
	allow := b.allowSet()
	msg := &telegram.Message{
		Chat:           telegram.Chat{ID: 12345},
		Text:           "my answer",
		ReplyToMessage: &telegram.Message{MessageID: 42},
	}
	b.handleReply(msg, allow)
	answer, ok := tk.result()
	if !ok || answer != "my answer" {
		t.Errorf("result = (%q,%v), want (my answer,true)", answer, ok)
	}
}

func TestHandleReplyRejectsDisallowedChat(t *testing.T) {
	cfg := testConfig()
	_, b, cleanup := startTestBroker(t, cfg)
	defer cleanup()

	tk := b.reg.create("12345", nil)
	allow := b.allowSet()
	// Reply from a chat not in the allow-set.
	msg := &telegram.Message{
		Chat: telegram.Chat{ID: 99999},
		Text: "intruder",
	}
	b.handleReply(msg, allow)
	if _, ok := tk.result(); ok {
		t.Error("ticket should not resolve from a disallowed chat")
	}
}

func TestOffsetPersistence(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "getUpdates.offset")
	writeOffset(path, 1234)
	if got := readOffset(path); got != 1234 {
		t.Errorf("readOffset = %d, want 1234", got)
	}
	// Missing file -> 0.
	if got := readOffset(filepath.Join(dir, "missing")); got != 0 {
		t.Errorf("readOffset(missing) = %d, want 0", got)
	}
}

func TestLockfileSingleton(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.lock")
	l1, err := acquireLock(path)
	if err != nil {
		t.Fatalf("first acquireLock: %v", err)
	}
	// Second acquire should fail because this process holds it.
	if _, err := acquireLock(path); err == nil {
		t.Error("second acquireLock should fail while live PID holds lock")
	}
	l1.release()
	// After release, acquire should succeed again.
	l2, err := acquireLock(path)
	if err != nil {
		t.Fatalf("acquireLock after release: %v", err)
	}
	l2.release()
}

func TestStaleLockReclaimed(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broker.lock")
	// Write a PID that is essentially certain not to be alive.
	if err := os.WriteFile(path, []byte("999999"), 0o644); err != nil {
		t.Fatal(err)
	}
	l, err := acquireLock(path)
	if err != nil {
		t.Fatalf("acquireLock over stale lock: %v", err)
	}
	defer l.release()
	data, _ := os.ReadFile(path)
	if string(data) != strconv.Itoa(os.Getpid()) {
		t.Errorf("lock pid = %s, want %d", data, os.Getpid())
	}
}

func TestReaperRemovesOldTickets(t *testing.T) {
	r := newRegistry()
	tk := r.create("c1", nil)
	// Backdate it.
	tk.created = time.Now().Add(-time.Hour)
	r.reap(30 * time.Minute)
	if r.get(tk.id) != nil {
		t.Error("old ticket should have been reaped")
	}
}

func TestRunStartsAndServes(t *testing.T) {
	// Smoke test that Run binds, serves /health, and shuts down on Close.
	t.Setenv("NOTI_TEST", "1")
	t.Setenv("CLAUDE_PLUGIN_DATA", t.TempDir())

	// Bind a known ephemeral port first, then hand its number to Run via cfg.
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("probe listen: %v", err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	_ = probe.Close()

	cfg := testConfig()
	cfg.Broker.Port = port

	done := make(chan error, 1)
	go func() { done <- Run(cfg) }()

	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	var resp *http.Response
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		resp, err = http.Get(base + "/health")
		if err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatalf("GET /health never succeeded: %v", err)
	}
	resp.Body.Close()

	select {
	case err := <-done:
		t.Fatalf("Run returned early: %v", err)
	default:
	}
}
