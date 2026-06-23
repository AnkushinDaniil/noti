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
)

// raceBroker is a configurable httptest broker for the ask_user race tests. It
// records which endpoints were hit and lets each test pin the resolved /config
// ask block plus the /wait behaviour.
type raceBroker struct {
	srv *httptest.Server

	mu          sync.Mutex
	askCalled   bool
	cancelCalls []string // tickets passed to /cancel
	waitAnswer  string   // "" => stay pending forever

	waitOnce  sync.Once
	waitHitCh chan struct{} // closed the first time /wait is hit
}

// raceBrokerConfig is the resolved ask block the broker returns from /config.
type raceBrokerConfig struct {
	mode          string
	idle          int
	laptop        *bool
	requireLaptop *bool
}

func boolPtr(b bool) *bool { return &b }

func newRaceBroker(t *testing.T, cfg raceBrokerConfig, waitAnswer string) *raceBroker {
	t.Helper()
	rb := &raceBroker{waitAnswer: waitAnswer, waitHitCh: make(chan struct{})}
	mux := http.NewServeMux()

	mux.HandleFunc("/config", func(w http.ResponseWriter, r *http.Request) {
		ask := map[string]any{
			"mode":                 cfg.mode,
			"idle_timeout_seconds": cfg.idle,
		}
		if cfg.laptop != nil {
			ask["laptop"] = *cfg.laptop
		}
		if cfg.requireLaptop != nil {
			ask["require_laptop"] = *cfg.requireLaptop
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ask": ask})
	})

	mux.HandleFunc("/ask", func(w http.ResponseWriter, r *http.Request) {
		rb.mu.Lock()
		rb.askCalled = true
		rb.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ticket": "T123", "status": "pending"})
	})

	mux.HandleFunc("/wait", func(w http.ResponseWriter, r *http.Request) {
		rb.waitOnce.Do(func() { close(rb.waitHitCh) })
		rb.mu.Lock()
		ans := rb.waitAnswer
		rb.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		if ans == "" {
			// Stay pending; the poll window is short so the loop spins.
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "pending"})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"status": "answered", "answer": ans})
	})

	mux.HandleFunc("/cancel", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Ticket string `json:"ticket"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		rb.mu.Lock()
		rb.cancelCalls = append(rb.cancelCalls, body.Ticket)
		rb.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	rb.srv = srv
	return rb
}

func (rb *raceBroker) asked() bool {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return rb.askCalled
}

func (rb *raceBroker) cancels() []string {
	rb.mu.Lock()
	defer rb.mu.Unlock()
	return append([]string(nil), rb.cancelCalls...)
}

// clientDriver wires a server to in-memory pipes and acts as the MCP CLIENT:
// it reads every outbound JSON-RPC line, dispatches server-issued requests
// (elicitation/create) to a handler, captures notifications, and surfaces
// tool-call responses by id.
type clientDriver struct {
	t      *testing.T
	stdinW *io.PipeWriter
	srv    *server
	done   chan struct{}

	mu            sync.Mutex
	notifications []map[string]any
	elicitIDs     []string // request ids the server issued for elicitation/create

	respCh chan map[string]any // tool-call responses keyed by surfacing them in order

	// elicitHandler decides how to answer an elicitation/create request. It is
	// invoked in a goroutine with the server-issued request id. Returning a nil
	// map means "do not respond" (let the prompt linger).
	elicitHandler func(id string, params map[string]any) map[string]any
}

func newClientDriver(t *testing.T, s *server, handler func(id string, params map[string]any) map[string]any) *clientDriver {
	t.Helper()
	stdinR, stdinW := io.Pipe()
	outR, outW := io.Pipe()
	s.out = outW

	d := &clientDriver{
		t:             t,
		stdinW:        stdinW,
		srv:           s,
		done:          make(chan struct{}),
		respCh:        make(chan map[string]any, 16),
		elicitHandler: handler,
	}

	go func() {
		_ = s.serve(stdinR)
		_ = outW.Close()
	}()

	// Reader goroutine: consume the server's stdout, classify each message.
	go func() {
		defer close(d.done)
		sc := bufio.NewScanner(outR)
		sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
		for sc.Scan() {
			var m map[string]any
			if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
				continue
			}
			d.classify(m)
		}
	}()
	return d
}

func (d *clientDriver) classify(m map[string]any) {
	method, hasMethod := m["method"].(string)
	_, hasID := m["id"]

	switch {
	case hasMethod && method == "elicitation/create" && hasID:
		// Server-issued request to the client.
		id := jsonIDString(m["id"])
		params, _ := m["params"].(map[string]any)
		d.mu.Lock()
		d.elicitIDs = append(d.elicitIDs, id)
		d.mu.Unlock()
		if d.elicitHandler != nil {
			go func() {
				resp := d.elicitHandler(id, params)
				if resp != nil {
					d.replyToServer(m["id"], resp)
				}
			}()
		}
	case hasMethod && !hasID:
		// Notification from server (e.g. notifications/cancelled).
		d.mu.Lock()
		d.notifications = append(d.notifications, m)
		d.mu.Unlock()
	case !hasMethod && hasID:
		// Response to one of our requests (a tool-call reply).
		d.respCh <- m
	}
}

// replyToServer sends a JSON-RPC response (to a server-issued request) back
// over stdin, the way a real client would answer elicitation/create.
func (d *clientDriver) replyToServer(id any, result map[string]any) {
	msg := map[string]any{"jsonrpc": "2.0", "id": id, "result": result}
	data, _ := json.Marshal(msg)
	d.send(string(data))
}

func (d *clientDriver) send(line string) {
	if _, err := io.WriteString(d.stdinW, line+"\n"); err != nil {
		d.t.Fatalf("write stdin: %v", err)
	}
}

// callTool sends a tools/call request and waits for its response.
func (d *clientDriver) callTool(id int, body string, timeout time.Duration) map[string]any {
	d.t.Helper()
	d.send(body)
	select {
	case m := <-d.respCh:
		return m
	case <-time.After(timeout):
		d.t.Fatalf("timeout waiting for tool-call response id=%d", id)
		return nil
	}
}

func (d *clientDriver) getNotifications() []map[string]any {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]map[string]any(nil), d.notifications...)
}

func (d *clientDriver) elicitRequestIDs() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.elicitIDs...)
}

func (d *clientDriver) close() {
	_ = d.stdinW.Close()
	select {
	case <-d.done:
	case <-time.After(3 * time.Second):
		d.t.Errorf("server/reader did not stop after stdin close")
	}
}

// jsonIDString renders a JSON-RPC id (string or number) as a string.
func jsonIDString(v any) string {
	switch x := v.(type) {
	case string:
		return x
	case float64:
		// Server-issued ids are strings ("noti-req-N"); numbers are unexpected.
		return strings.TrimSuffix(jsonNumber(x), ".0")
	default:
		b, _ := json.Marshal(v)
		return string(b)
	}
}

func jsonNumber(f float64) string {
	b, _ := json.Marshal(f)
	return string(b)
}

// acceptChoice builds an elicitation accept result for a choice answer.
func acceptChoice(answer string) map[string]any {
	return map[string]any{"action": "accept", "content": map[string]any{"choice": answer}}
}

// acceptReply builds an elicitation accept result for a free-text answer.
func acceptReply(answer string) map[string]any {
	return map[string]any{"action": "accept", "content": map[string]any{"reply": answer}}
}

// newElicitServer builds a server with the elicitation capability advertised
// and a short ceiling so timeout cases return fast.
func newElicitServer(brokerURL string, ceiling time.Duration) *server {
	s := newServer(nil, brokerURL)
	s.clientElicitation = true
	s.askCeiling = ceiling
	return s
}

const askCallTimeout = 5 * time.Second

// Scenario 1: forward-all, laptop accepts first -> tool returns the laptop
// answer; the broker /cancel is called for the phone ticket.
func TestAskRaceForwardAllLaptopWins(t *testing.T) {
	rb := newRaceBroker(t, raceBrokerConfig{
		mode: "forward-all", idle: 1, laptop: boolPtr(true), requireLaptop: boolPtr(true),
	}, "") // phone never answers
	s := newElicitServer(rb.srv.URL, 3*time.Second)
	d := newClientDriver(t, s, func(id string, params map[string]any) map[string]any {
		// Wait until the phone has hit /wait, which guarantees the server has
		// already stored the ticket (it is set before the first poll). This
		// makes the laptop-wins path reliably exercise the /cancel of the
		// outstanding ticket.
		select {
		case <-rb.waitHitCh:
		case <-time.After(2 * time.Second):
		}
		return acceptChoice("ship")
	})
	defer d.close()

	resp := d.callTool(1, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Ship it?","options":["ship","hold"]}}}`, askCallTimeout)
	text, isErr := toolText(t, resp)
	if isErr {
		t.Fatalf("expected success, got isError (%s)", text)
	}
	if text != "ship" {
		t.Errorf("answer = %q, want ship", text)
	}
	// forward-all started the phone immediately, so a ticket exists and the
	// laptop winner must cancel it.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if len(rb.cancels()) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancels := rb.cancels()
	if len(cancels) == 0 || cancels[0] != "T123" {
		t.Errorf("expected /cancel for T123, got %v", cancels)
	}
}

// Scenario 2: timeout mode, laptop accepts before idle -> the broker /ask is
// NEVER called.
func TestAskRaceTimeoutLaptopBeforeIdle(t *testing.T) {
	rb := newRaceBroker(t, raceBrokerConfig{
		mode: "timeout", idle: 30, laptop: boolPtr(true), requireLaptop: boolPtr(true),
	}, "")
	s := newElicitServer(rb.srv.URL, 3*time.Second)
	d := newClientDriver(t, s, func(id string, params map[string]any) map[string]any {
		return acceptReply("from laptop")
	})
	defer d.close()

	resp := d.callTool(1, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Free text?"}}}`, askCallTimeout)
	text, isErr := toolText(t, resp)
	if isErr {
		t.Fatalf("expected success, got isError (%s)", text)
	}
	if text != "from laptop" {
		t.Errorf("answer = %q, want 'from laptop'", text)
	}
	// idle is 30s but the laptop answered immediately; /ask must not fire.
	time.Sleep(100 * time.Millisecond)
	if rb.asked() {
		t.Errorf("broker /ask should NOT be called when laptop answers before idle")
	}
}

// Scenario 3: timeout mode, idle elapses -> /ask called, phone answers, tool
// returns the phone answer, server sends notifications/cancelled with the
// elicitation request id, and a late laptop accept is dropped.
func TestAskRaceTimeoutIdlePhoneWins(t *testing.T) {
	rb := newRaceBroker(t, raceBrokerConfig{
		mode: "timeout", idle: 1, laptop: boolPtr(true), requireLaptop: boolPtr(true),
	}, "phone-answer")

	// The laptop "answers" late: hold until released, then accept (must be dropped).
	release := make(chan struct{})
	s := newElicitServer(rb.srv.URL, 5*time.Second)
	d := newClientDriver(t, s, func(id string, params map[string]any) map[string]any {
		<-release // wait until the phone has already won
		return acceptChoice("late-laptop")
	})
	defer d.close()

	resp := d.callTool(1, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Ship?","options":["a","b"]}}}`, askCallTimeout)
	text, isErr := toolText(t, resp)
	if isErr {
		t.Fatalf("expected success, got isError (%s)", text)
	}
	if text != "phone-answer" {
		t.Errorf("answer = %q, want phone-answer", text)
	}
	if !rb.asked() {
		t.Errorf("expected broker /ask to be called after idle elapsed")
	}

	// The server should have sent notifications/cancelled for the elicitation id.
	wantID := d.elicitRequestIDs()
	if len(wantID) == 0 {
		t.Fatalf("no elicitation/create was issued")
	}
	notifs := d.getNotifications()
	var found bool
	for _, n := range notifs {
		if n["method"] == "notifications/cancelled" {
			params, _ := n["params"].(map[string]any)
			if params["requestId"] == wantID[0] {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("expected notifications/cancelled with requestId=%q; notifs=%v", wantID[0], notifs)
	}

	// Now release the late laptop answer; it must be dropped (no panic, no
	// change to the already-returned result). The tool already returned, so we
	// just confirm no second response arrives.
	close(release)
	select {
	case extra := <-d.respCh:
		t.Errorf("unexpected second tool response after phone win: %#v", extra)
	case <-time.After(200 * time.Millisecond):
		// good: late laptop answer was dropped.
	}
}

// Scenario 4: laptop declines -> escalate to phone (broker /ask called even
// before idle would elapse) and the phone answer wins.
func TestAskRaceDeclineEscalatesToPhone(t *testing.T) {
	rb := newRaceBroker(t, raceBrokerConfig{
		mode: "timeout", idle: 30, laptop: boolPtr(true), requireLaptop: boolPtr(true),
	}, "phone-after-decline")
	s := newElicitServer(rb.srv.URL, 5*time.Second)
	d := newClientDriver(t, s, func(id string, params map[string]any) map[string]any {
		return map[string]any{"action": "decline"}
	})
	defer d.close()

	resp := d.callTool(1, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Ship?","options":["a","b"]}}}`, askCallTimeout)
	text, isErr := toolText(t, resp)
	if isErr {
		t.Fatalf("expected success, got isError (%s)", text)
	}
	if text != "phone-after-decline" {
		t.Errorf("answer = %q, want phone-after-decline", text)
	}
	// Even though idle is 30s, the decline must have escalated to the phone.
	if !rb.asked() {
		t.Errorf("expected /ask after laptop decline (escalation before idle)")
	}
}

// Scenario 5: hard-require, client did NOT advertise elicitation -> ask_user
// returns isError and the broker /ask is never called.
func TestAskRaceHardRequireNoCapability(t *testing.T) {
	rb := newRaceBroker(t, raceBrokerConfig{
		mode: "timeout", idle: 1, laptop: boolPtr(true), requireLaptop: boolPtr(true),
	}, "phone")
	s := newServer(nil, rb.srv.URL) // no clientElicitation
	s.askCeiling = 3 * time.Second
	d := newClientDriver(t, s, nil)
	defer d.close()

	resp := d.callTool(1, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Ship?"}}}`, askCallTimeout)
	text, isErr := toolText(t, resp)
	if !isErr {
		t.Fatalf("expected isError true for hard-require without capability, got %q", text)
	}
	if !strings.Contains(text, "elicitation") {
		t.Errorf("expected elicitation update message, got %q", text)
	}
	time.Sleep(100 * time.Millisecond)
	if rb.asked() {
		t.Errorf("broker /ask must NOT be called on the hard-require error path")
	}
}

// Scenario 6: require_laptop:false + no capability -> phone-only path works.
func TestAskRaceRequireLaptopFalsePhoneOnly(t *testing.T) {
	rb := newRaceBroker(t, raceBrokerConfig{
		mode: "timeout", idle: 1, laptop: boolPtr(true), requireLaptop: boolPtr(false),
	}, "phone-only-answer")
	s := newServer(nil, rb.srv.URL) // no clientElicitation
	s.askCeiling = 5 * time.Second
	d := newClientDriver(t, s, nil)
	defer d.close()

	resp := d.callTool(1, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"ask_user","arguments":{"question":"Ship?"}}}`, askCallTimeout)
	text, isErr := toolText(t, resp)
	if isErr {
		t.Fatalf("expected success on phone-only fallback, got isError (%s)", text)
	}
	if text != "phone-only-answer" {
		t.Errorf("answer = %q, want phone-only-answer", text)
	}
	if !rb.asked() {
		t.Errorf("expected /ask to be called on phone-only path")
	}
	// No elicitation should have been issued (no capability).
	if ids := d.elicitRequestIDs(); len(ids) != 0 {
		t.Errorf("expected no elicitation/create, got %v", ids)
	}
}
