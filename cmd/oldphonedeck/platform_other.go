//go:build !linux

package main

// Placeholders for platforms whose backend isn't written yet (Windows, macOS).
// The server still runs: the page loads, and features without a backend just stay empty.
// To port a feature, add e.g. apps_windows.go and narrow the build tag above.

import (
	"errors"
	"os"
	"os/exec"
	"runtime"
)

var errUnsupported = errors.New("not supported on " + runtime.GOOS + " yet")

func openURL(u string) error {
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", u)
	case "darwin":
		cmd = exec.Command("open", u)
	default:
		return errUnsupported
	}
	return cmd.Start()
}

func hasClaudeApp() bool { return runtime.GOOS == "darwin" || runtime.GOOS == "windows" }

func hostName() string {
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "Computer"
}

func readNetBytes() (rx, tx uint64, ok bool) { return 0, 0, false }
func readBatteries() []Battery               { return []Battery{} }

func listApps() []App                      { return []App{} }
func LaunchApp(path string) error          { return errUnsupported }
func AppIcon(path string) ([]byte, string) { return nil, "" }

type Music struct{}

func NewMusic() *Music                    { return &Music{} }
func (m *Music) Start()                   {}
func (m *Music) Snapshot() map[string]any { return map[string]any{} }
func (m *Music) Artwork() []byte          { return nil }
func (m *Music) Control(cmd string)       {}
func (m *Music) OpenPlayer()              {}
