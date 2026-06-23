package broker

import (
	"sync"
	"time"
)

// ticket is a single pending question awaiting an answer from the phone.
type ticket struct {
	id        string
	chatID    string
	messageID int      // Telegram message id of the question (for reply matching)
	options   []string // inline-keyboard options, if any
	created   time.Time

	mu        sync.Mutex
	answer    string
	answered  bool
	cancelled bool
	done      chan struct{} // closed exactly once when resolved
}

// registry is the thread-safe store of pending tickets.
type registry struct {
	mu      sync.Mutex
	tickets map[string]*ticket
	seq     uint64
}

// newRegistry returns an empty registry.
func newRegistry() *registry {
	return &registry{tickets: make(map[string]*ticket)}
}

// create allocates and stores a new ticket with the given chat and options.
func (r *registry) create(chatID string, options []string) *ticket {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	t := &ticket{
		id:      newTicketID(r.seq),
		chatID:  chatID,
		options: options,
		created: time.Now(),
		done:    make(chan struct{}),
	}
	r.tickets[t.id] = t
	return t
}

// get returns the ticket for id, or nil if unknown.
func (r *registry) get(id string) *ticket {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.tickets[id]
}

// setMessageID records the Telegram message id for reply-to matching.
func (r *registry) setMessageID(id string, messageID int) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if t := r.tickets[id]; t != nil {
		t.mu.Lock()
		t.messageID = messageID
		t.mu.Unlock()
	}
}

// resolve answers a ticket. First writer wins; later calls are no-ops and
// return false. Returns true only for the resolving call.
func (r *registry) resolve(id, answer string) bool {
	t := r.get(id)
	if t == nil {
		return false
	}
	return t.resolve(answer)
}

// resolve marks the ticket answered (first-wins) and closes done.
func (t *ticket) resolve(answer string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.answered || t.cancelled {
		return false
	}
	t.answer = answer
	t.answered = true
	close(t.done)
	return true
}

// cancel marks the ticket cancelled so future replies are dropped. Idempotent.
func (t *ticket) cancel() {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.answered || t.cancelled {
		return
	}
	t.cancelled = true
	close(t.done)
}

// isCancelled reports whether the ticket was cancelled.
func (t *ticket) isCancelled() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.cancelled
}

// result returns the current answer and answered flag under lock.
func (t *ticket) result() (string, bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.answer, t.answered
}

// pendingForChat returns the single pending (un-answered, un-cancelled) ticket
// for chatID, or nil if there are zero or more than one.
func (r *registry) pendingForChat(chatID string) *ticket {
	r.mu.Lock()
	defer r.mu.Unlock()
	var found *ticket
	for _, t := range r.tickets {
		t.mu.Lock()
		pending := !t.answered && !t.cancelled
		match := t.chatID == chatID
		t.mu.Unlock()
		if pending && match {
			if found != nil {
				return nil // ambiguous: more than one pending
			}
			found = t
		}
	}
	return found
}

// byMessageID returns the pending ticket whose question message id matches.
func (r *registry) byMessageID(messageID int) *ticket {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tickets {
		t.mu.Lock()
		match := t.messageID == messageID && !t.answered && !t.cancelled
		t.mu.Unlock()
		if match {
			return t
		}
	}
	return nil
}

// reap removes tickets older than maxAge.
func (r *registry) reap(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	r.mu.Lock()
	defer r.mu.Unlock()
	for id, t := range r.tickets {
		if t.created.Before(cutoff) {
			delete(r.tickets, id)
		}
	}
}

// pendingCount returns the number of un-answered, un-cancelled tickets.
func (r *registry) pendingCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for _, t := range r.tickets {
		t.mu.Lock()
		if !t.answered && !t.cancelled {
			n++
		}
		t.mu.Unlock()
	}
	return n
}
