// Package broker implements the noti broker daemon: a loopback HTTP server that
// dispatches notifications and questions to Telegram and a single getUpdates
// poll goroutine that resolves pending questions from phone replies.
package broker

import (
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/AnkushinDaniil/noti/internal/config"
	"github.com/AnkushinDaniil/noti/internal/telegram"
)

const (
	ticketMaxAge   = 30 * time.Minute
	reapInterval   = time.Minute
	maxWaitSeconds = 55
	pollTimeoutSec = 25
)

// broker holds the running daemon's shared state.
type broker struct {
	cfg      *config.Config
	tg       *telegram.Client
	reg      *registry
	dataDir  string
	testMode bool

	connMu    sync.Mutex
	connected bool
}

// isTestMode reports whether NOTI_TEST=1 is set (no network poll, /test/inject
// enabled, telegram sends recorded as no-ops).
func isTestMode() bool {
	return os.Getenv("NOTI_TEST") == "1"
}

// newTicketID derives a short, unique ticket id from a sequence counter.
func newTicketID(seq uint64) string {
	return fmt.Sprintf("t%d-%d", time.Now().UnixNano(), seq)
}

// Run starts the broker daemon, binding cfg.Broker.Host:Port (Port 0 picks an
// ephemeral port). It enforces a single-instance lockfile and blocks until the
// process is signaled to stop.
func Run(cfg *config.Config) error {
	test := isTestMode()
	dataDir := config.DataDir()

	lock, err := acquireLock(filepath.Join(dataDir, "broker.lock"))
	if err != nil {
		return err
	}
	defer lock.release()

	var tg *telegram.Client
	if test {
		tg = telegram.NewTest()
	} else {
		tg = telegram.New(cfg.Telegram.BotToken)
	}

	b := &broker{
		cfg:      cfg,
		tg:       tg,
		reg:      newRegistry(),
		dataDir:  dataDir,
		testMode: test,
	}

	addr := net.JoinHostPort(cfg.Broker.Host, strconv.Itoa(cfg.Broker.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("broker listen on %s: %w", addr, err)
	}

	srv := &http.Server{Handler: b.handler()}

	stop := make(chan struct{})
	var reaperWg sync.WaitGroup
	reaperWg.Add(1)
	go func() {
		defer reaperWg.Done()
		b.runReaper(stop)
	}()

	if !test {
		go b.pollUpdates(stop)
	}

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-sig
		_ = srv.Close()
	}()

	log.Printf("noti broker listening on %s (test=%v)", ln.Addr(), test)
	err = srv.Serve(ln)
	close(stop)
	reaperWg.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}

// allowSet returns the set of chat ids permitted to resolve tickets: the
// telegram default chat plus every routing chat id.
func (b *broker) allowSet() map[string]bool {
	set := make(map[string]bool)
	if id := b.cfg.Telegram.DefaultChatID; id != "" {
		set[id] = true
	}
	for _, r := range b.cfg.Routing {
		if r.ChatID != "" {
			set[r.ChatID] = true
		}
	}
	return set
}

func (b *broker) setConnected(v bool) {
	b.connMu.Lock()
	b.connected = v
	b.connMu.Unlock()
}

func (b *broker) isConnected() bool {
	b.connMu.Lock()
	defer b.connMu.Unlock()
	return b.connected
}

// runReaper periodically removes tickets older than ticketMaxAge.
func (b *broker) runReaper(stop <-chan struct{}) {
	ticker := time.NewTicker(reapInterval)
	defer ticker.Stop()
	for {
		select {
		case <-stop:
			return
		case <-ticker.C:
			b.reg.reap(ticketMaxAge)
		}
	}
}
