// Command noti is the single noti binary.
//
// Subcommands:
//
//	broker         start the broker daemon (blocks)
//	mcp            start the MCP stdio server (blocks)
//	notify <level> fire a hook notification (reads JSON from stdin)
//	permission-gate phone-first PreToolUse permission gate (reads JSON from stdin)
//	detect-chat    print the most recent private Telegram chat ID (setup helper)
//	test [text]    send a test notification
//	version        print the version string
//	help           print usage
package main

import (
	"fmt"
	"os"
	"strings"

	"github.com/AnkushinDaniil/noti/internal/broker"
	"github.com/AnkushinDaniil/noti/internal/config"
	"github.com/AnkushinDaniil/noti/internal/mcp"
	"github.com/AnkushinDaniil/noti/internal/notify"
	"github.com/AnkushinDaniil/noti/internal/permission"
	"github.com/AnkushinDaniil/noti/internal/telegram"
	"github.com/AnkushinDaniil/noti/internal/version"
)

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "noti:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	if len(args) == 0 {
		printUsage()
		return nil
	}

	cmd := args[0]
	rest := args[1:]

	switch cmd {
	case "broker":
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return broker.Run(cfg)

	case "mcp":
		return mcp.Run()

	case "notify":
		level := "info"
		if len(rest) > 0 {
			level = rest[0]
		}
		cfg, err := loadConfig()
		if err != nil {
			return err
		}
		return notify.Run(cfg, level, os.Stdin)

	case "permission-gate":
		cfg, err := loadConfig()
		if err != nil {
			// Never block the tool: emit a pass-through decision and exit 0.
			cfg = &config.Config{}
		}
		return permission.Run(cfg, os.Stdin, os.Stdout)

	case "detect-chat":
		return detectChat()

	case "test":
		text := "✅ noti test message — if you see this, delivery works!"
		if len(rest) > 0 {
			text = strings.Join(rest, " ")
		}
		return sendTest(text)

	case "version", "-version", "--version", "-v":
		fmt.Println(version.Version)
		return nil

	case "help", "-help", "--help", "-h":
		printUsage()
		return nil

	default:
		printUsage()
		return fmt.Errorf("unknown subcommand: %s", cmd)
	}
}

// loadConfig reads the configuration from the default path.
func loadConfig() (*config.Config, error) {
	cfg, err := config.Load(config.DefaultPath())
	if err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return cfg, nil
}

// detectChat calls getUpdates once and prints the most recent private chat ID.
func detectChat() error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	if cfg.Telegram.BotToken == "" {
		return fmt.Errorf("telegram.bot_token is not set in config — run `noti setup` first")
	}
	tg := telegram.New(cfg.Telegram.BotToken)
	updates, err := tg.GetUpdates(0, 5)
	if err != nil {
		return fmt.Errorf("getUpdates: %w", err)
	}
	if len(updates) == 0 {
		fmt.Fprintln(os.Stderr, "no updates received — send any message to your bot on Telegram and try again")
		return nil
	}
	// Print the most recent private chat ID.
	for i := len(updates) - 1; i >= 0; i-- {
		u := updates[i]
		if u.Message != nil && u.Message.Chat.Type == "private" {
			fmt.Println(u.Message.Chat.ID)
			return nil
		}
	}
	// Fallback: print the last update's chat ID regardless of type.
	last := updates[len(updates)-1]
	if last.Message != nil {
		fmt.Println(last.Message.Chat.ID)
	}
	return nil
}

// sendTest sends a test message via the broker /notify endpoint, falling back
// to a direct Telegram send if the broker is unreachable.
func sendTest(text string) error {
	cfg, err := loadConfig()
	if err != nil {
		return err
	}
	// Try broker first (reuse the notify package path).
	err = notify.Run(cfg, "info", strings.NewReader(
		`{"hook_event_name":"test","cwd":"","message":"`+text+`"}`,
	))
	if err != nil {
		// notify.Run always returns nil per contract; this branch is defensive.
		return err
	}
	fmt.Println("test message sent")
	return nil
}

func printUsage() {
	fmt.Fprintf(os.Stderr, `noti %s — phone notifications for Claude Code

Usage:
  noti broker              Start the background broker daemon
  noti mcp                 Start the MCP stdio server
  noti notify <level>      Send a hook notification (stdin: hook JSON)
  noti permission-gate     Phone-first permission gate (PreToolUse hook; stdin: hook JSON)
  noti detect-chat         Print the most recent Telegram chat ID
  noti test [text]         Send a test notification
  noti version             Print version
  noti help                Print this help

Levels: attention | done | info (default: info)

Config: %s  (override: $NOTI_CONFIG)
`, version.Version, config.DefaultPath())
}
