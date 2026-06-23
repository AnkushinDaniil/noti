package mcp

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/AnkushinDaniil/noti/internal/version"
)

// newServer builds a server wired to brokerURL, writing to out.
func newServer(out io.Writer, brokerURL string) *server {
	return &server{
		out:       out,
		brokerURL: strings.TrimRight(brokerURL, "/"),
		httpc:     &http.Client{Timeout: 5 * time.Second},
		pending:   make(map[string]chan rpcRequest),
	}
}

// driver wires a server to an in-memory stdin pipe and a reader over stdout.
type driver struct {
	t      *testing.T
	stdinW *io.PipeWriter
	outR   *bufio.Scanner
	done   chan struct{}
}

func newDriver(t *testing.T, s *server) *driver {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	outR, outW := io.Pipe()
	s.out = outW

	d := &driver{
		t:      t,
		stdinW: stdinW,
		outR:   bufio.NewScanner(outR),
		done:   make(chan struct{}),
	}
	d.outR.Buffer(make([]byte, 0, 64*1024), 1<<20)

	go func() {
		_ = s.serve(stdinR)
		_ = outW.Close()
		close(d.done)
	}()
	return d
}

func (d *driver) send(msg string) {
	d.t.Helper()
	if _, err := io.WriteString(d.stdinW, msg+"\n"); err != nil {
		d.t.Fatalf("write stdin: %v", err)
	}
}

// recv reads one JSON-RPC line from stdout, decoded into a generic map.
func (d *driver) recv() map[string]any {
	d.t.Helper()
	if !d.outR.Scan() {
		if err := d.outR.Err(); err != nil {
			d.t.Fatalf("read stdout: %v", err)
		}
		d.t.Fatalf("no response on stdout")
	}
	var m map[string]any
	if err := json.Unmarshal(d.outR.Bytes(), &m); err != nil {
		d.t.Fatalf("decode response %q: %v", d.outR.Text(), err)
	}
	return m
}

func (d *driver) close() {
	_ = d.stdinW.Close()
	select {
	case <-d.done:
	case <-time.After(2 * time.Second):
		d.t.Fatalf("server did not stop after stdin close")
	}
}

func TestInitialize(t *testing.T) {
	s := newServer(nil, defaultBrokerURL)
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2025-06-18","capabilities":{"elicitation":{}}}}`)
	resp := d.recv()

	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected result object, got %#v", resp)
	}
	if got := result["protocolVersion"]; got != "2025-06-18" {
		t.Errorf("protocolVersion = %v, want 2025-06-18", got)
	}
	si, ok := result["serverInfo"].(map[string]any)
	if !ok {
		t.Fatalf("missing serverInfo: %#v", result)
	}
	if si["name"] != "noti" {
		t.Errorf("serverInfo.name = %v, want noti", si["name"])
	}
	if si["version"] != version.Version {
		t.Errorf("serverInfo.version = %v, want %v", si["version"], version.Version)
	}
	caps, ok := result["capabilities"].(map[string]any)
	if !ok {
		t.Fatalf("missing capabilities: %#v", result)
	}
	if _, ok := caps["tools"]; !ok {
		t.Errorf("capabilities missing tools key: %#v", caps)
	}
	if !s.clientElicitation {
		t.Errorf("expected clientElicitation flag to be set")
	}
}

func TestInitializeDefaultsProtocolVersion(t *testing.T) {
	s := newServer(nil, defaultBrokerURL)
	d := newDriver(t, s)
	defer d.close()

	// Non-string protocolVersion -> server should fall back to its default.
	d.send(`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":1,"capabilities":{}}}`)
	resp := d.recv()
	result := resp["result"].(map[string]any)
	if got := result["protocolVersion"]; got != defaultProtocolVer {
		t.Errorf("protocolVersion = %v, want %v", got, defaultProtocolVer)
	}
	if s.clientElicitation {
		t.Errorf("clientElicitation should be false when not advertised")
	}
}

func TestPingAndInitializedNotification(t *testing.T) {
	s := newServer(nil, defaultBrokerURL)
	d := newDriver(t, s)
	defer d.close()

	// Notification: no response expected.
	d.send(`{"jsonrpc":"2.0","method":"notifications/initialized"}`)
	d.send(`{"jsonrpc":"2.0","id":7,"method":"ping"}`)
	resp := d.recv()
	if _, ok := resp["result"].(map[string]any); !ok {
		t.Fatalf("ping expected empty result object, got %#v", resp)
	}
	// The ping id (7) should be echoed; the notification produced nothing.
	if resp["id"] != float64(7) {
		t.Errorf("ping id = %v, want 7", resp["id"])
	}
}

func TestUnknownMethod(t *testing.T) {
	s := newServer(nil, defaultBrokerURL)
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":3,"method":"does/not/exist"}`)
	resp := d.recv()
	e, ok := resp["error"].(map[string]any)
	if !ok {
		t.Fatalf("expected error, got %#v", resp)
	}
	if e["code"] != float64(-32601) {
		t.Errorf("error code = %v, want -32601", e["code"])
	}
	if resp["id"] != float64(3) {
		t.Errorf("error id = %v, want 3", resp["id"])
	}
}

func TestToolsList(t *testing.T) {
	s := newServer(nil, defaultBrokerURL)
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`)
	resp := d.recv()
	result := resp["result"].(map[string]any)
	tools, ok := result["tools"].([]any)
	if !ok {
		t.Fatalf("missing tools array: %#v", result)
	}
	want := map[string]bool{
		"ask_user": false, "wait_for_reply": false, "notify": false,
		"send_file": false, "send_image": false,
	}
	for _, tv := range tools {
		tm := tv.(map[string]any)
		name, _ := tm["name"].(string)
		if _, ok := want[name]; ok {
			want[name] = true
		}
		if _, ok := tm["inputSchema"]; !ok {
			t.Errorf("tool %q missing inputSchema", name)
		}
		if _, ok := tm["description"]; !ok {
			t.Errorf("tool %q missing description", name)
		}
	}
	for name, found := range want {
		if !found {
			t.Errorf("tools/list missing tool %q", name)
		}
	}
}

// stubBroker is an httptest broker returning a pending /ask ticket and an
// answered /wait response.
func stubBroker(t *testing.T, askStatus, waitStatus, answer string) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/ask", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": "T123", "status": askStatus})
	})
	mux.HandleFunc("/wait", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"status": waitStatus, "answer": answer})
	})
	mux.HandleFunc("/notify", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sent": true, "channels": []string{"telegram"}})
	})
	mux.HandleFunc("/send_file", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"sent": true})
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// toolText extracts the first text content and isError flag from a tools/call
// response.
func toolText(t *testing.T, resp map[string]any) (string, bool) {
	t.Helper()
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("expected tools/call result, got %#v", resp)
	}
	content, ok := result["content"].([]any)
	if !ok || len(content) == 0 {
		t.Fatalf("missing content: %#v", result)
	}
	first := content[0].(map[string]any)
	isErr, _ := result["isError"].(bool)
	text, _ := first["text"].(string)
	return text, isErr
}

func TestAskUserAnswered(t *testing.T) {
	broker := stubBroker(t, "pending", "answered", "yes")
	s := newServer(nil, broker.URL)
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":10,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Ship it?","options":["yes","no"]}}}`)
	resp := d.recv()
	text, isErr := toolText(t, resp)
	if isErr {
		t.Errorf("expected isError false, got true (%s)", text)
	}
	if text != "yes" {
		t.Errorf("answer text = %q, want yes", text)
	}
}

func TestAskUserPendingHandsBackTicket(t *testing.T) {
	broker := stubBroker(t, "pending", "pending", "")
	s := newServer(nil, broker.URL)
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":11,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Ship it?"}}}`)
	resp := d.recv()
	text, isErr := toolText(t, resp)
	if isErr {
		t.Errorf("expected isError false, got true")
	}
	if !strings.Contains(text, "T123") || !strings.Contains(text, "wait_for_reply") {
		t.Errorf("expected ticket hint with T123, got %q", text)
	}
}

func TestWaitForReplyStatuses(t *testing.T) {
	cases := []struct {
		status  string
		answer  string
		wantSub string
		isErr   bool
	}{
		{"answered", "done", "done", false},
		{"pending", "", "Still waiting", false},
		{"unknown_ticket", "", "Unknown ticket", true},
	}
	for _, tc := range cases {
		t.Run(tc.status, func(t *testing.T) {
			broker := stubBroker(t, "pending", tc.status, tc.answer)
			s := newServer(nil, broker.URL)
			d := newDriver(t, s)
			defer d.close()

			d.send(`{"jsonrpc":"2.0","id":12,"method":"tools/call","params":{"name":"wait_for_reply","arguments":{"ticket":"T123"}}}`)
			resp := d.recv()
			text, isErr := toolText(t, resp)
			if isErr != tc.isErr {
				t.Errorf("isError = %v, want %v (%s)", isErr, tc.isErr, text)
			}
			if !strings.Contains(text, tc.wantSub) {
				t.Errorf("text = %q, want substring %q", text, tc.wantSub)
			}
		})
	}
}

func TestNotify(t *testing.T) {
	broker := stubBroker(t, "pending", "pending", "")
	s := newServer(nil, broker.URL)
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":13,"method":"tools/call","params":{"name":"notify","arguments":{"text":"hi"}}}`)
	resp := d.recv()
	text, isErr := toolText(t, resp)
	if isErr {
		t.Errorf("notify isError true: %s", text)
	}
	if !strings.Contains(text, "sent") && !strings.Contains(text, "Sent") {
		t.Errorf("notify text = %q", text)
	}
}

func TestSendFile(t *testing.T) {
	broker := stubBroker(t, "pending", "pending", "")
	s := newServer(nil, broker.URL)
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":14,"method":"tools/call","params":{"name":"send_file","arguments":{"path":"/tmp/x.txt"}}}`)
	resp := d.recv()
	text, isErr := toolText(t, resp)
	if isErr {
		t.Errorf("send_file isError true: %s", text)
	}
	if !strings.Contains(text, "/tmp/x.txt") {
		t.Errorf("send_file text = %q", text)
	}
}

func TestBrokerUnreachable(t *testing.T) {
	// Point at a closed port (no server listening).
	s := newServer(nil, "http://127.0.0.1:1")
	d := newDriver(t, s)
	defer d.close()

	d.send(`{"jsonrpc":"2.0","id":15,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"x"}}}`)
	resp := d.recv()
	text, isErr := toolText(t, resp)
	if !isErr {
		t.Errorf("expected isError true when broker unreachable")
	}
	if !strings.Contains(text, "broker") {
		t.Errorf("expected broker hint, got %q", text)
	}
}

func TestConcurrentRequestsSerializedOutput(t *testing.T) {
	s := newServer(nil, defaultBrokerURL)
	d := newDriver(t, s)
	defer d.close()

	const n = 20
	for i := 0; i < n; i++ {
		d.send(`{"jsonrpc":"2.0","id":` + itoa(i) + `,"method":"ping"}`)
	}
	seen := make(map[float64]bool)
	for i := 0; i < n; i++ {
		resp := d.recv()
		id, ok := resp["id"].(float64)
		if !ok {
			t.Fatalf("response missing numeric id: %#v", resp)
		}
		if _, dup := seen[id]; dup {
			t.Errorf("duplicate id %v", id)
		}
		seen[id] = true
		if _, ok := resp["result"]; !ok {
			t.Errorf("ping response missing result: %#v", resp)
		}
	}
	if len(seen) != n {
		t.Errorf("got %d distinct responses, want %d", len(seen), n)
	}
}

func TestResponseRoutingToPending(t *testing.T) {
	// A response message (no method, has id+result) should not produce output;
	// it routes to the pending map (Step 2 infrastructure).
	s := newServer(nil, defaultBrokerURL)

	ch := make(chan rpcRequest, 1)
	s.pendingMu.Lock()
	s.pending["99"] = ch
	s.pendingMu.Unlock()

	stdinR, stdinW := io.Pipe()
	var out strings.Builder
	var mu sync.Mutex
	s.out = writerFunc(func(p []byte) (int, error) {
		mu.Lock()
		defer mu.Unlock()
		return out.WriteString(string(p))
	})

	done := make(chan struct{})
	go func() { _ = s.serve(stdinR); close(done) }()

	_, _ = io.WriteString(stdinW, `{"jsonrpc":"2.0","id":99,"result":{"ok":true}}`+"\n")

	select {
	case got := <-ch:
		if string(got.ID) != "99" {
			t.Errorf("routed response id = %s, want 99", got.ID)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("response was not routed to pending channel")
	}

	_ = stdinW.Close()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if out.String() != "" {
		t.Errorf("response message should produce no stdout, got %q", out.String())
	}
}

// writerFunc adapts a function to io.Writer.
type writerFunc func([]byte) (int, error)

func (f writerFunc) Write(p []byte) (int, error) { return f(p) }

// itoa is a tiny stdlib-free int formatter for test message ids.
func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	var b [20]byte
	pos := len(b)
	for i > 0 {
		pos--
		b[pos] = byte('0' + i%10)
		i /= 10
	}
	return string(b[pos:])
}
