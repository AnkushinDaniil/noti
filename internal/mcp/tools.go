package mcp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
)

// toolDefs returns the 5 tool definitions advertised by tools/list.
func toolDefs() []map[string]any {
	strProp := func(desc string) map[string]any {
		return map[string]any{"type": "string", "description": desc}
	}
	return []map[string]any{
		{
			"name":        "ask_user",
			"description": "Ask the human; returns the answer or a ticket. Use instead of guessing.",
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

	var ask askResponse
	if err := s.brokerPost("/ask", map[string]any{
		"question": a.Question,
		"options":  a.Options,
		"project":  a.Project,
	}, &ask); err != nil {
		s.toolResult(id, brokerHint, true)
		return
	}

	// Loop one /wait cycle: answered -> answer; pending -> hand back the ticket.
	var wait waitResponse
	if err := s.brokerPost("/wait", map[string]any{
		"ticket":  ask.Ticket,
		"timeout": 50,
	}, &wait); err != nil {
		s.toolResult(id, brokerHint, true)
		return
	}

	switch wait.Status {
	case "answered":
		s.toolResult(id, wait.Answer, false)
	default:
		s.toolResult(id, fmt.Sprintf("No reply yet. Call wait_for_reply with ticket=%q to keep waiting.", ask.Ticket), false)
	}
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
