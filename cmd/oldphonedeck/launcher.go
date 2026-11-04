package main

import (
	"encoding/json"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// App is one launchable application. Path is an opaque, platform-specific identifier
// (a .desktop file on Linux, an .app bundle on macOS, a shortcut on Windows).
type App struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

var (
	appsMu     sync.Mutex
	appsCache  []App
	appsCached time.Time
)

// ListApps returns the installed apps, cached briefly (it's called on every launch/icon request).
func ListApps() []App {
	appsMu.Lock()
	defer appsMu.Unlock()
	if appsCache == nil || time.Since(appsCached) > 30*time.Second {
		appsCache = listApps()
		appsCached = time.Now()
	}
	return appsCache
}

// IsKnownApp: only paths that appear in our own listing may be launched or rendered.
func IsKnownApp(path string) bool {
	for _, a := range ListApps() {
		if a.Path == path {
			return true
		}
	}
	return false
}

// Which apps the deck shows, in order. Stored on the computer so every browser on the phone sees the same list.
var launcherFile = filepath.Join(configDir, "launcher.json")

// LauncherSelected returns nil when never configured (the phone then suggests editing).
func LauncherSelected() []string {
	b, err := os.ReadFile(launcherFile)
	if err != nil {
		return nil
	}
	var paths []string
	if json.Unmarshal(b, &paths) != nil {
		return nil
	}
	return paths
}

func LauncherSave(paths []string) (int, error) {
	known := map[string]bool{}
	for _, a := range ListApps() {
		known[a.Path] = true
	}
	clean := []string{}
	for _, p := range paths {
		if known[p] {
			clean = append(clean, p)
		}
	}
	if err := os.MkdirAll(configDir, 0o700); err != nil {
		return 0, err
	}
	b, _ := json.MarshalIndent(clean, "", "  ")
	return len(clean), os.WriteFile(launcherFile, b, 0o600)
}

// ClaudeAsk starts a new Claude conversation with text (or just opens Claude when text is empty).
// Uses the desktop app's claude:// link when a handler is registered, otherwise claude.ai in the browser.
func ClaudeAsk(text string) {
	q := url.Values{}
	if text != "" {
		q.Set("q", text)
	}
	link := "https://claude.ai/new"
	if hasClaudeApp() {
		q.Set("surface", "chat")
		link = "claude://claude.ai/new"
	}
	if len(q) > 0 {
		// Spaces as %20, not "+": some deep-link parsers don't treat "+" as a space.
		link += "?" + strings.ReplaceAll(q.Encode(), "+", "%20")
	}
	if err := openURL(link); err != nil {
		logf("Take note: could not open %s: %v", link, err)
	}
}
