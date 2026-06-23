// Package telegram is a minimal stdlib-only Telegram Bot API client used by
// the noti broker. It sends plain text (no parse_mode) and supports a test
// mode that records sends to an Outbox instead of hitting the network.
package telegram

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// baseURL is the Telegram Bot API endpoint. Overridable in tests.
var baseURL = "https://api.telegram.org"

// ErrConflict is returned by GetUpdates when the API responds HTTP 409,
// indicating another getUpdates consumer is active.
var ErrConflict = errors.New("telegram getUpdates 409 conflict")

// Sent records a single outbound action in test mode.
type Sent struct {
	Method    string
	ChatID    string
	Text      string
	Path      string
	Caption   string
	MessageID int
}

// Client is a Telegram Bot API client.
type Client struct {
	token string
	httpc *http.Client
	test  bool

	mu        sync.Mutex
	Outbox    []Sent
	nextMsgID int
}

// New returns a network-backed client for the given bot token.
func New(token string) *Client {
	return &Client{
		token: token,
		httpc: &http.Client{Timeout: 15 * time.Second},
	}
}

// NewTest returns a test client that performs no network I/O. Sends are
// recorded to Outbox and return synthetic-but-monotonic message IDs.
func NewTest() *Client {
	return &Client{test: true}
}

// Update is a Telegram update object.
type Update struct {
	UpdateID      int            `json:"update_id"`
	Message       *Message       `json:"message,omitempty"`
	CallbackQuery *CallbackQuery `json:"callback_query,omitempty"`
}

// Message is a Telegram message.
type Message struct {
	MessageID      int      `json:"message_id"`
	Chat           Chat     `json:"chat"`
	Text           string   `json:"text"`
	ReplyToMessage *Message `json:"reply_to_message,omitempty"`
}

// Chat is a Telegram chat.
type Chat struct {
	ID        int64  `json:"id"`
	Type      string `json:"type"`
	FirstName string `json:"first_name"`
	Title     string `json:"title"`
	Username  string `json:"username"`
}

// CallbackQuery is an inline-keyboard button press.
type CallbackQuery struct {
	ID      string   `json:"id"`
	Data    string   `json:"data"`
	Message *Message `json:"message,omitempty"`
}

// method builds the full API URL for a Bot API method.
func (c *Client) method(name string) string {
	return fmt.Sprintf("%s/bot%s/%s", baseURL, c.token, name)
}

// recordSent appends an entry to the Outbox under the client lock.
func (c *Client) recordSent(s Sent) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.nextMsgID++
	s.MessageID = c.nextMsgID
	c.Outbox = append(c.Outbox, s)
	return c.nextMsgID
}

// SendMessage sends a plain-text message, optionally with a reply markup
// (e.g. an inline keyboard). Returns the new message ID.
func (c *Client) SendMessage(chatID, text string, replyMarkup any) (int, error) {
	if c.test {
		return c.recordSent(Sent{Method: "sendMessage", ChatID: chatID, Text: text}), nil
	}
	payload := map[string]any{"chat_id": chatID, "text": text}
	if replyMarkup != nil {
		payload["reply_markup"] = replyMarkup
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return 0, err
	}
	resp, err := c.httpc.Post(c.method("sendMessage"), "application/json", bytes.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	var out struct {
		OK     bool `json:"ok"`
		Result struct {
			MessageID int `json:"message_id"`
		} `json:"result"`
		Description string `json:"description"`
	}
	if err := decodeResult(resp, &out); err != nil {
		return 0, err
	}
	if !out.OK {
		return 0, fmt.Errorf("telegram sendMessage failed: %s", out.Description)
	}
	return out.Result.MessageID, nil
}

// GetUpdates polls for updates from the given offset using long polling.
// In test mode it returns no updates.
func (c *Client) GetUpdates(offset, timeoutSec int) ([]Update, error) {
	if c.test {
		return nil, nil
	}
	httpc := &http.Client{Timeout: time.Duration(timeoutSec+10) * time.Second}
	q := url.Values{}
	q.Set("offset", strconv.Itoa(offset))
	q.Set("timeout", strconv.Itoa(timeoutSec))
	resp, err := httpc.Get(c.method("getUpdates") + "?" + q.Encode())
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusConflict {
		return nil, ErrConflict
	}
	var out struct {
		OK          bool     `json:"ok"`
		Result      []Update `json:"result"`
		Description string   `json:"description"`
	}
	if err := decodeResult(resp, &out); err != nil {
		return nil, err
	}
	if !out.OK {
		return nil, fmt.Errorf("telegram getUpdates failed: %s", out.Description)
	}
	return out.Result, nil
}

// AnswerCallback acknowledges an inline-keyboard callback query.
func (c *Client) AnswerCallback(id string) error {
	if c.test {
		c.recordSent(Sent{Method: "answerCallbackQuery"})
		return nil
	}
	body, err := json.Marshal(map[string]any{"callback_query_id": id})
	if err != nil {
		return err
	}
	resp, err := c.httpc.Post(c.method("answerCallbackQuery"), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// EditMessageText replaces the text of an existing message.
func (c *Client) EditMessageText(chatID string, messageID int, text string) error {
	if c.test {
		c.recordSent(Sent{Method: "editMessageText", ChatID: chatID, Text: text, MessageID: messageID})
		return nil
	}
	body, err := json.Marshal(map[string]any{
		"chat_id":    chatID,
		"message_id": messageID,
		"text":       text,
	})
	if err != nil {
		return err
	}
	resp, err := c.httpc.Post(c.method("editMessageText"), "application/json", bytes.NewReader(body))
	if err != nil {
		return err
	}
	resp.Body.Close()
	return nil
}

// SendDocument uploads a file as a document via multipart/form-data.
func (c *Client) SendDocument(chatID, path, caption string) error {
	return c.sendFile("sendDocument", "document", chatID, path, caption)
}

// SendPhoto uploads an image via multipart/form-data.
func (c *Client) SendPhoto(chatID, path, caption string) error {
	return c.sendFile("sendPhoto", "photo", chatID, path, caption)
}

// sendFile is the shared multipart upload path for documents and photos.
func (c *Client) sendFile(method, field, chatID, path, caption string) error {
	if c.test {
		c.recordSent(Sent{Method: method, ChatID: chatID, Path: path, Caption: caption})
		return nil
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()

	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	if err := w.WriteField("chat_id", chatID); err != nil {
		return err
	}
	if caption != "" {
		if err := w.WriteField("caption", caption); err != nil {
			return err
		}
	}
	part, err := w.CreateFormFile(field, filepath.Base(path))
	if err != nil {
		return err
	}
	if _, err := io.Copy(part, f); err != nil {
		return err
	}
	if err := w.Close(); err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, c.method(method), &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", w.FormDataContentType())
	resp, err := c.httpc.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	var out struct {
		OK          bool   `json:"ok"`
		Description string `json:"description"`
	}
	if err := decodeResult(resp, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("telegram %s failed: %s", method, out.Description)
	}
	return nil
}

// IsImage reports whether path has a common image extension. Useful for
// callers deciding between SendPhoto and SendDocument.
func IsImage(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".jpg", ".jpeg", ".png", ".gif", ".webp", ".bmp":
		return true
	default:
		return false
	}
}

// decodeResult decodes a JSON response body into v.
func decodeResult(resp *http.Response, v any) error {
	return json.NewDecoder(resp.Body).Decode(v)
}
