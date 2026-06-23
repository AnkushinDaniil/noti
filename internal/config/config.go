// Package config defines the noti configuration schema and loading/resolution
// helpers shared across the broker, mcp, notify, and main packages.
package config

import (
	"encoding/json"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
)

// Telegram holds Telegram bot credentials and the default destination chat.
type Telegram struct {
	BotToken      string `json:"bot_token"`
	DefaultChatID string `json:"default_chat_id"`
}

// Channels holds optional non-Telegram delivery webhooks.
type Channels struct {
	DiscordWebhook string `json:"discord_webhook"`
	SlackWebhook   string `json:"slack_webhook"`
}

// Permissions configures the (Step 2) permission-gate behavior.
type Permissions struct {
	Enabled        bool     `json:"enabled"`
	TimeoutSeconds int      `json:"timeout_seconds"`
	Tools          []string `json:"tools"`
}

// DefaultGatedTools is the built-in set of tools the permission-gate routes to
// the phone when no explicit tools list is configured.
var DefaultGatedTools = []string{"Bash", "Write", "Edit", "NotebookEdit"}

// Ask configures the question/answer behavior. Pointer fields distinguish
// "unset" (nil, inherit from a lower-priority layer) from an explicit value.
type Ask struct {
	Mode               string       `json:"mode"` // "timeout" | "forward-all"
	IdleTimeoutSeconds int          `json:"idle_timeout_seconds"`
	Laptop             *bool        `json:"laptop,omitempty"`
	RequireLaptop      *bool        `json:"require_laptop,omitempty"`
	Permissions        *Permissions `json:"permissions,omitempty"`
}

// Route is a single routing rule mapping a project/path to a channel + chat.
type Route struct {
	Match     string `json:"match"`
	MatchType string `json:"match_type"` // "project" (basename cwd) | "path_glob"
	Channel   string `json:"channel"`
	ChatID    string `json:"chat_id"`
	Ask       *Ask   `json:"ask,omitempty"`
}

// BrokerCfg is the broker daemon's bind address.
type BrokerCfg struct {
	Host string `json:"host"`
	Port int    `json:"port"`
}

// Config is the full noti configuration.
type Config struct {
	Telegram Telegram  `json:"telegram"`
	Channels Channels  `json:"channels"`
	Routing  []Route   `json:"routing"`
	Broker   BrokerCfg `json:"broker"`
	Ask      *Ask      `json:"ask,omitempty"`
}

// Target is the resolved delivery destination.
type Target struct{ Channel, ChatID string }

func boolPtr(b bool) *bool { return &b }

// defaultAsk returns the built-in default Ask configuration.
func defaultAsk() Ask {
	return Ask{
		Mode:               "timeout",
		IdleTimeoutSeconds: 30,
		Laptop:             boolPtr(true),
		RequireLaptop:      boolPtr(true),
		Permissions: &Permissions{
			Enabled:        true,
			TimeoutSeconds: 30,
			Tools:          append([]string(nil), DefaultGatedTools...),
		},
	}
}

// DefaultPath returns $NOTI_CONFIG, or ~/.config/noti/config.json otherwise.
func DefaultPath() string {
	if p := os.Getenv("NOTI_CONFIG"); p != "" {
		return p
	}
	home, err := os.UserHomeDir()
	if err != nil {
		home = ""
	}
	return filepath.Join(home, ".config", "noti", "config.json")
}

// Load reads the config file at path. A missing file is not an error: a
// defaults-applied Config is returned. Malformed JSON returns an error.
func Load(path string) (*Config, error) {
	c := &Config{}
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			applyDefaults(c)
			return c, nil
		}
		return nil, err
	}
	if err := json.Unmarshal(data, c); err != nil {
		return nil, err
	}
	applyDefaults(c)
	return c, nil
}

// applyDefaults fills in zero-valued broker fields.
func applyDefaults(c *Config) {
	if c.Broker.Host == "" {
		c.Broker.Host = "127.0.0.1"
	}
	if c.Broker.Port == 0 {
		c.Broker.Port = 7432
	}
}

// matchRoute returns the first routing rule matching project (basename of cwd)
// or a path_glob against project. Returns nil if no rule matches.
func (c *Config) matchRoute(project string) *Route {
	for i := range c.Routing {
		r := &c.Routing[i]
		switch r.MatchType {
		case "path_glob":
			if ok, err := filepath.Match(r.Match, project); err == nil && ok {
				return r
			}
		default: // "project" or unset: exact basename match
			if r.Match == project {
				return r
			}
		}
	}
	return nil
}

// ResolveTarget picks the delivery target. An explicit channel+chatID wins;
// otherwise the first matching routing rule; otherwise the telegram default.
func (c *Config) ResolveTarget(project, channel, chatID string) Target {
	if channel != "" && chatID != "" {
		return Target{Channel: channel, ChatID: chatID}
	}
	if r := c.matchRoute(project); r != nil {
		t := Target{Channel: r.Channel, ChatID: r.ChatID}
		if t.Channel == "" {
			t.Channel = "telegram"
		}
		if t.ChatID == "" {
			t.ChatID = c.Telegram.DefaultChatID
		}
		return t
	}
	return Target{Channel: "telegram", ChatID: c.Telegram.DefaultChatID}
}

// ResolveAsk merges the matched route's Ask over the top-level Ask over the
// built-in defaults.
func (c *Config) ResolveAsk(project string) Ask {
	result := defaultAsk()
	mergeAsk(&result, c.Ask)
	if r := c.matchRoute(project); r != nil {
		mergeAsk(&result, r.Ask)
	}
	// Guarantee the permission-gate has a non-empty gated tool set even when a
	// config layer supplied a Permissions block without a tools list.
	if result.Permissions == nil {
		result.Permissions = &Permissions{Enabled: true, TimeoutSeconds: 30}
	}
	if len(result.Permissions.Tools) == 0 {
		result.Permissions.Tools = append([]string(nil), DefaultGatedTools...)
	}
	return result
}

// mergeAsk overlays the non-empty fields of src onto dst.
func mergeAsk(dst *Ask, src *Ask) {
	if src == nil {
		return
	}
	if src.Mode != "" {
		dst.Mode = src.Mode
	}
	if src.IdleTimeoutSeconds != 0 {
		dst.IdleTimeoutSeconds = src.IdleTimeoutSeconds
	}
	if src.Laptop != nil {
		v := *src.Laptop
		dst.Laptop = &v
	}
	if src.RequireLaptop != nil {
		v := *src.RequireLaptop
		dst.RequireLaptop = &v
	}
	if src.Permissions != nil {
		p := *src.Permissions
		dst.Permissions = &p
	}
}

// DataDir returns $CLAUDE_PLUGIN_DATA or ~/.local/state/noti, creating it.
func DataDir() string {
	dir := os.Getenv("CLAUDE_PLUGIN_DATA")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			home = ""
		}
		dir = filepath.Join(home, ".local", "state", "noti")
	}
	_ = os.MkdirAll(dir, 0o755)
	return dir
}
