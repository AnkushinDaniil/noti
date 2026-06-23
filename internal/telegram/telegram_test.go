package telegram

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNewTestRecordsSendMessage(t *testing.T) {
	c := NewTest()
	id, err := c.SendMessage("123", "hello", nil)
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if id <= 0 {
		t.Errorf("message id = %d, want > 0", id)
	}
	if len(c.Outbox) != 1 {
		t.Fatalf("Outbox len = %d, want 1", len(c.Outbox))
	}
	got := c.Outbox[0]
	if got.Method != "sendMessage" || got.ChatID != "123" || got.Text != "hello" {
		t.Errorf("Outbox[0] = %+v, want sendMessage/123/hello", got)
	}
	if got.MessageID != id {
		t.Errorf("Outbox MessageID = %d, want %d", got.MessageID, id)
	}
}

func TestNewTestSendMessageMonotonicIDs(t *testing.T) {
	c := NewTest()
	id1, _ := c.SendMessage("1", "a", nil)
	id2, _ := c.SendMessage("1", "b", nil)
	if id2 <= id1 {
		t.Errorf("ids not monotonic: %d then %d", id1, id2)
	}
}

func TestGetUpdatesTestModeEmpty(t *testing.T) {
	c := NewTest()
	ups, err := c.GetUpdates(0, 25)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(ups) != 0 {
		t.Errorf("GetUpdates test mode len = %d, want 0", len(ups))
	}
}

// withBaseURL swaps the package baseURL for the duration of a test.
func withBaseURL(t *testing.T, u string) {
	t.Helper()
	orig := baseURL
	baseURL = u
	t.Cleanup(func() { baseURL = orig })
}

func TestGetUpdatesConflict(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	c := New("tok")
	_, err := c.GetUpdates(0, 1)
	if err != ErrConflict {
		t.Errorf("err = %v, want ErrConflict", err)
	}
}

func TestGetUpdatesParsesResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":[{"update_id":5,"message":{"message_id":9,"text":"hi","chat":{"id":42,"type":"private"}}}]}`))
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	c := New("tok")
	ups, err := c.GetUpdates(0, 1)
	if err != nil {
		t.Fatalf("GetUpdates: %v", err)
	}
	if len(ups) != 1 || ups[0].UpdateID != 5 || ups[0].Message == nil {
		t.Fatalf("unexpected updates: %+v", ups)
	}
	if ups[0].Message.Chat.ID != 42 || ups[0].Message.Text != "hi" {
		t.Errorf("message parse = %+v", ups[0].Message)
	}
}

func TestSendMessageNetwork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.Path, "sendMessage") {
			t.Errorf("unexpected path %q", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true,"result":{"message_id":77}}`))
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	c := New("tok")
	id, err := c.SendMessage("123", "hello", map[string]any{"inline_keyboard": []any{}})
	if err != nil {
		t.Fatalf("SendMessage: %v", err)
	}
	if id != 77 {
		t.Errorf("message id = %d, want 77", id)
	}
}

func TestSendDocumentMultipart(t *testing.T) {
	var gotField, gotChatID, gotCaption string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		gotChatID = r.FormValue("chat_id")
		gotCaption = r.FormValue("caption")
		if r.MultipartForm != nil {
			if fhs := r.MultipartForm.File["document"]; len(fhs) > 0 {
				gotField = fhs[0].Filename
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()
	withBaseURL(t, srv.URL)

	dir := t.TempDir()
	fpath := filepath.Join(dir, "report.txt")
	if err := os.WriteFile(fpath, []byte("file body"), 0o600); err != nil {
		t.Fatal(err)
	}

	c := New("tok")
	if err := c.SendDocument("123", fpath, "a caption"); err != nil {
		t.Fatalf("SendDocument: %v", err)
	}
	if gotChatID != "123" {
		t.Errorf("chat_id = %q, want 123", gotChatID)
	}
	if gotCaption != "a caption" {
		t.Errorf("caption = %q, want 'a caption'", gotCaption)
	}
	if gotField != "report.txt" {
		t.Errorf("uploaded filename = %q, want report.txt", gotField)
	}
}

func TestSendDocumentTestMode(t *testing.T) {
	c := NewTest()
	if err := c.SendDocument("123", "/tmp/x.pdf", "cap"); err != nil {
		t.Fatalf("SendDocument test mode: %v", err)
	}
	if len(c.Outbox) != 1 || c.Outbox[0].Method != "sendDocument" || c.Outbox[0].Path != "/tmp/x.pdf" {
		t.Errorf("Outbox = %+v", c.Outbox)
	}
}

func TestIsImage(t *testing.T) {
	cases := map[string]bool{
		"a.png": true, "b.JPG": true, "c.jpeg": true, "d.gif": true,
		"e.txt": false, "f.pdf": false, "g": false,
	}
	for path, want := range cases {
		if got := IsImage(path); got != want {
			t.Errorf("IsImage(%q) = %v, want %v", path, got, want)
		}
	}
}
