package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadMissingFileAppliesDefaults(t *testing.T) {
	path := filepath.Join(t.TempDir(), "does-not-exist.json")
	c, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing file: unexpected error %v", err)
	}
	if c.Broker.Host != "127.0.0.1" {
		t.Errorf("Broker.Host = %q, want 127.0.0.1", c.Broker.Host)
	}
	if c.Broker.Port != 7432 {
		t.Errorf("Broker.Port = %d, want 7432", c.Broker.Port)
	}
}

func TestLoadMalformedJSON(t *testing.T) {
	path := filepath.Join(t.TempDir(), "bad.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Fatal("Load malformed JSON: expected error, got nil")
	}
}

func TestLoadAppliesBrokerDefaultsWhenPartial(t *testing.T) {
	path := filepath.Join(t.TempDir(), "c.json")
	if err := os.WriteFile(path, []byte(`{"broker":{"port":9999}}`), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Broker.Host != "127.0.0.1" {
		t.Errorf("Broker.Host = %q, want default 127.0.0.1", c.Broker.Host)
	}
	if c.Broker.Port != 9999 {
		t.Errorf("Broker.Port = %d, want 9999", c.Broker.Port)
	}
}

func TestDefaultPathEnvOverride(t *testing.T) {
	t.Setenv("NOTI_CONFIG", "/custom/noti.json")
	if got := DefaultPath(); got != "/custom/noti.json" {
		t.Errorf("DefaultPath() = %q, want /custom/noti.json", got)
	}
}

func TestDefaultPathFallback(t *testing.T) {
	t.Setenv("NOTI_CONFIG", "")
	home, _ := os.UserHomeDir()
	want := filepath.Join(home, ".config", "noti", "config.json")
	if got := DefaultPath(); got != want {
		t.Errorf("DefaultPath() = %q, want %q", got, want)
	}
}

func TestResolveTargetExplicit(t *testing.T) {
	c := &Config{Telegram: Telegram{DefaultChatID: "111"}}
	got := c.ResolveTarget("proj", "telegram", "555")
	if got.Channel != "telegram" || got.ChatID != "555" {
		t.Errorf("explicit ResolveTarget = %+v, want {telegram 555}", got)
	}
}

func TestResolveTargetDefault(t *testing.T) {
	c := &Config{Telegram: Telegram{DefaultChatID: "111"}}
	got := c.ResolveTarget("proj", "", "")
	if got.Channel != "telegram" || got.ChatID != "111" {
		t.Errorf("default ResolveTarget = %+v, want {telegram 111}", got)
	}
}

func TestResolveTargetProjectRoute(t *testing.T) {
	c := &Config{
		Telegram: Telegram{DefaultChatID: "111"},
		Routing: []Route{
			{Match: "myproj", MatchType: "project", Channel: "telegram", ChatID: "222"},
		},
	}
	got := c.ResolveTarget("myproj", "", "")
	if got.ChatID != "222" {
		t.Errorf("project route ResolveTarget chatID = %q, want 222", got.ChatID)
	}
	// Non-matching project falls through to default.
	other := c.ResolveTarget("nope", "", "")
	if other.ChatID != "111" {
		t.Errorf("non-matching ResolveTarget chatID = %q, want default 111", other.ChatID)
	}
}

func TestResolveTargetPathGlob(t *testing.T) {
	c := &Config{
		Telegram: Telegram{DefaultChatID: "111"},
		Routing: []Route{
			{Match: "work-*", MatchType: "path_glob", Channel: "telegram", ChatID: "333"},
		},
	}
	got := c.ResolveTarget("work-app", "", "")
	if got.ChatID != "333" {
		t.Errorf("path_glob ResolveTarget chatID = %q, want 333", got.ChatID)
	}
}

func TestResolveAskDefaults(t *testing.T) {
	c := &Config{}
	a := c.ResolveAsk("proj")
	if a.Mode != "timeout" {
		t.Errorf("Mode = %q, want timeout", a.Mode)
	}
	if a.IdleTimeoutSeconds != 30 {
		t.Errorf("IdleTimeoutSeconds = %d, want 30", a.IdleTimeoutSeconds)
	}
	if a.Laptop == nil || !*a.Laptop {
		t.Errorf("Laptop = %v, want true", a.Laptop)
	}
	if a.RequireLaptop == nil || !*a.RequireLaptop {
		t.Errorf("RequireLaptop = %v, want true", a.RequireLaptop)
	}
	if a.Permissions == nil || !a.Permissions.Enabled || a.Permissions.TimeoutSeconds != 30 {
		t.Errorf("Permissions = %+v, want {true 30}", a.Permissions)
	}
}

func TestResolveAskTopLevelOverridesDefaults(t *testing.T) {
	c := &Config{Ask: &Ask{Mode: "forward-all", IdleTimeoutSeconds: 99}}
	a := c.ResolveAsk("proj")
	if a.Mode != "forward-all" {
		t.Errorf("Mode = %q, want forward-all", a.Mode)
	}
	if a.IdleTimeoutSeconds != 99 {
		t.Errorf("IdleTimeoutSeconds = %d, want 99", a.IdleTimeoutSeconds)
	}
	// Untouched fields keep defaults.
	if a.Laptop == nil || !*a.Laptop {
		t.Errorf("Laptop = %v, want default true", a.Laptop)
	}
}

func TestResolveAskRouteBeatsTopLevel(t *testing.T) {
	laptopFalse := false
	c := &Config{
		Ask: &Ask{Mode: "forward-all", IdleTimeoutSeconds: 99},
		Routing: []Route{
			{
				Match: "myproj", MatchType: "project",
				Ask: &Ask{IdleTimeoutSeconds: 5, Laptop: &laptopFalse},
			},
		},
	}
	a := c.ResolveAsk("myproj")
	// Route overrides idle timeout and laptop.
	if a.IdleTimeoutSeconds != 5 {
		t.Errorf("IdleTimeoutSeconds = %d, want 5 (route)", a.IdleTimeoutSeconds)
	}
	if a.Laptop == nil || *a.Laptop {
		t.Errorf("Laptop = %v, want false (route)", a.Laptop)
	}
	// Mode not set in route -> inherits top-level forward-all.
	if a.Mode != "forward-all" {
		t.Errorf("Mode = %q, want forward-all (top-level)", a.Mode)
	}
}

func TestResolveAskImmutability(t *testing.T) {
	c := &Config{Ask: &Ask{Permissions: &Permissions{Enabled: false, TimeoutSeconds: 7}}}
	a := c.ResolveAsk("proj")
	a.Permissions.TimeoutSeconds = 100
	// Mutating the returned copy must not affect the source config.
	if c.Ask.Permissions.TimeoutSeconds != 7 {
		t.Errorf("source mutated: TimeoutSeconds = %d, want 7", c.Ask.Permissions.TimeoutSeconds)
	}
}

func TestDataDirEnvOverride(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("CLAUDE_PLUGIN_DATA", dir)
	if got := DataDir(); got != dir {
		t.Errorf("DataDir() = %q, want %q", got, dir)
	}
}
