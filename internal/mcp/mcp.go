// Package mcp implements the noti MCP stdio server: a newline-delimited
// JSON-RPC 2.0 server speaking over stdin/stdout. It logs to stderr only
// (stdout is the JSON-RPC channel) and talks to the broker over HTTP.
package mcp

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/AnkushinDaniil/noti/internal/version"
)

const (
	defaultBrokerURL    = "http://127.0.0.1:7432"
	defaultProtocolVer  = "2025-06-18"
	brokerClientTimeout = 55 * time.Second
)

// rpcRequest is an incoming JSON-RPC 2.0 message. A message with a method is a
// request (id present) or notification (id absent); a message with no method
// but an id and result/error is a response to a server-issued request.
type rpcRequest struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   json.RawMessage `json:"error,omitempty"`
}

// rpcResponse is an outgoing JSON-RPC 2.0 response.
type rpcResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Result  any             `json:"result,omitempty"`
	Error   *rpcError       `json:"error,omitempty"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// server holds the MCP server state.
type server struct {
	out       io.Writer
	writeMu   sync.Mutex
	brokerURL string
	httpc     *http.Client

	// clientElicitation records whether the client advertised an elicitation
	// capability during initialize. Stored for Step 2 (unused now).
	clientElicitation bool

	// pending maps a server-issued request id to a channel awaiting its
	// response. Infrastructure for Step 2 elicitation; unused now.
	pendingMu sync.Mutex
	pending   map[string]chan rpcRequest
}

// Run starts the MCP stdio server, reading from os.Stdin and writing to
// os.Stdout. It blocks until stdin reaches EOF.
func Run() error {
	log.SetOutput(os.Stderr)
	log.SetPrefix("noti-mcp: ")

	brokerURL := os.Getenv("NOTI_BROKER_URL")
	if brokerURL == "" {
		brokerURL = defaultBrokerURL
	}
	brokerURL = strings.TrimRight(brokerURL, "/")

	s := &server{
		out:       os.Stdout,
		brokerURL: brokerURL,
		httpc:     &http.Client{Timeout: brokerClientTimeout},
		pending:   make(map[string]chan rpcRequest),
	}
	return s.serve(os.Stdin)
}

// serve runs the read loop over r, dispatching each JSON-RPC message. Requests
// are dispatched in their own goroutine so that long-running tool calls do not
// block the reader.
func (s *server) serve(r io.Reader) error {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	var wg sync.WaitGroup
	for sc.Scan() {
		line := bytes.TrimSpace(sc.Bytes())
		if len(line) == 0 {
			continue
		}
		raw := make([]byte, len(line))
		copy(raw, line)

		var msg rpcRequest
		if err := json.Unmarshal(raw, &msg); err != nil {
			log.Printf("failed to parse message: %v", err)
			continue
		}

		if msg.Method == "" && len(msg.ID) > 0 && (len(msg.Result) > 0 || len(msg.Error) > 0) {
			// Response to a server-issued request: route to the pending map.
			s.routeResponse(msg)
			continue
		}

		wg.Add(1)
		go func(m rpcRequest) {
			defer wg.Done()
			s.dispatch(m)
		}(msg)
	}
	wg.Wait()
	if err := sc.Err(); err != nil {
		return fmt.Errorf("mcp stdin read: %w", err)
	}
	return nil
}

// routeResponse delivers a server-issued request's response to its waiter, if
// any. Infrastructure for Step 2 elicitation.
func (s *server) routeResponse(msg rpcRequest) {
	key := string(msg.ID)
	s.pendingMu.Lock()
	ch, ok := s.pending[key]
	if ok {
		delete(s.pending, key)
	}
	s.pendingMu.Unlock()
	if ok {
		ch <- msg
	}
}

// dispatch routes a request or notification to its handler.
func (s *server) dispatch(msg rpcRequest) {
	switch msg.Method {
	case "initialize":
		s.handleInitialize(msg)
	case "notifications/initialized":
		// Notification: nothing to do.
	case "ping":
		s.reply(msg.ID, map[string]any{})
	case "tools/list":
		s.reply(msg.ID, map[string]any{"tools": toolDefs()})
	case "tools/call":
		s.handleToolsCall(msg)
	default:
		// Notifications (no id) are ignored; requests get method-not-found.
		if len(msg.ID) > 0 {
			s.replyError(msg.ID, -32601, "method not found: "+msg.Method)
		}
	}
}

// initializeParams is the subset of initialize params we care about.
type initializeParams struct {
	ProtocolVersion json.RawMessage            `json:"protocolVersion"`
	Capabilities    map[string]json.RawMessage `json:"capabilities"`
}

func (s *server) handleInitialize(msg rpcRequest) {
	var p initializeParams
	if len(msg.Params) > 0 {
		_ = json.Unmarshal(msg.Params, &p)
	}

	// Echo the client's protocolVersion if it is a string; else default.
	protocolVersion := defaultProtocolVer
	if len(p.ProtocolVersion) > 0 {
		var v string
		if err := json.Unmarshal(p.ProtocolVersion, &v); err == nil && v != "" {
			protocolVersion = v
		}
	}

	// Record the client's elicitation capability (for Step 2).
	if _, ok := p.Capabilities["elicitation"]; ok {
		s.clientElicitation = true
	}

	s.reply(msg.ID, map[string]any{
		"protocolVersion": protocolVersion,
		"capabilities":    map[string]any{"tools": map[string]any{}},
		"serverInfo": map[string]any{
			"name":    "noti",
			"version": version.Version,
		},
	})
}

// reply writes a successful JSON-RPC response.
func (s *server) reply(id json.RawMessage, result any) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: idOrNull(id), Result: result})
}

// replyError writes a JSON-RPC error response.
func (s *server) replyError(id json.RawMessage, code int, message string) {
	s.write(rpcResponse{JSONRPC: "2.0", ID: idOrNull(id), Error: &rpcError{Code: code, Message: message}})
}

func idOrNull(id json.RawMessage) json.RawMessage {
	if len(id) == 0 {
		return json.RawMessage("null")
	}
	return id
}

// write serializes resp to a single newline-delimited line on stdout, guarded
// by a mutex so concurrent handlers never interleave their output.
func (s *server) write(resp rpcResponse) {
	data, err := json.Marshal(resp)
	if err != nil {
		log.Printf("failed to marshal response: %v", err)
		return
	}
	data = append(data, '\n')
	s.writeMu.Lock()
	defer s.writeMu.Unlock()
	if _, err := s.out.Write(data); err != nil {
		log.Printf("failed to write response: %v", err)
	}
}
