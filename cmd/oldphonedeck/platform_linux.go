package main

import (
	"bufio"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"

	"github.com/godbus/dbus/v5"
)

// openURL hands a link to the desktop's default handler (browser, claude:// app…).
func openURL(u string) error {
	cmd := exec.Command("xdg-open", u)
	if err := cmd.Start(); err != nil {
		cmd = exec.Command("gio", "open", u)
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	go cmd.Wait()
	return nil
}

func hasClaudeApp() bool {
	out, err := exec.Command("xdg-mime", "query", "default", "x-scheme-handler/claude").Output()
	return err == nil && strings.TrimSpace(string(out)) != ""
}

// hostName prefers the "pretty" name set in GNOME Settings → About (e.g. "Mello's ThinkPad").
func hostName() string {
	if f, err := os.Open("/etc/machine-info"); err == nil {
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			if v, ok := strings.CutPrefix(sc.Text(), "PRETTY_HOSTNAME="); ok {
				if v = strings.Trim(v, `"'`); v != "" {
					return v
				}
			}
		}
	}
	if h, err := os.Hostname(); err == nil {
		return h
	}
	return "Computer"
}

// readNetBytes sums received/sent bytes over physical-looking interfaces from /proc/net/dev.
func readNetBytes() (rx, tx uint64, ok bool) {
	f, err := os.Open("/proc/net/dev")
	if err != nil {
		return 0, 0, false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		name, rest, found := strings.Cut(sc.Text(), ":")
		if !found {
			continue
		}
		name = strings.TrimSpace(name)
		if isVirtualIface(name) {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) < 9 {
			continue
		}
		r, _ := strconv.ParseUint(fields[0], 10, 64)
		t, _ := strconv.ParseUint(fields[8], 10, 64)
		rx += r
		tx += t
	}
	return rx, tx, true
}

func isVirtualIface(name string) bool {
	if name == "lo" {
		return true
	}
	for _, p := range []string{"docker", "veth", "br-", "virbr", "vnet", "tun", "tap", "wg", "tailscale", "zt"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// readBatteries asks UPower, which covers the laptop battery, Logitech/Bluetooth mice and keyboards,
// and Bluetooth headphones that report their level (AirPods included, via BlueZ).
func readBatteries() []Battery {
	list := []Battery{}
	conn, err := dbus.SystemBus()
	if err != nil {
		return list
	}
	var paths []dbus.ObjectPath
	if conn.Object("org.freedesktop.UPower", "/org/freedesktop/UPower").
		Call("org.freedesktop.UPower.EnumerateDevices", 0).Store(&paths) != nil {
		return list
	}
	for _, p := range paths {
		var props map[string]dbus.Variant
		if conn.Object("org.freedesktop.UPower", p).
			Call("org.freedesktop.DBus.Properties.GetAll", 0, "org.freedesktop.UPower.Device").Store(&props) != nil {
			continue
		}
		typ, _ := props["Type"].Value().(uint32)
		kind := upowerKind(typ)
		if kind == "" {
			continue
		}
		if present, ok := props["IsPresent"].Value().(bool); ok && !present {
			continue
		}
		pct, _ := props["Percentage"].Value().(float64)
		state, _ := props["State"].Value().(uint32) // 1 charging, 4 fully charged
		name, _ := props["Model"].Value().(string)
		if kind == "laptop" {
			name = "Laptop"
		} else if name == "" {
			name = strings.ToUpper(kind[:1]) + kind[1:]
		}
		kind = refineKind(kind, name)
		list = append(list, Battery{Name: shortName(name), Kind: kind, Level: int(pct + 0.5), Charging: state == 1})
	}
	return list
}

// upowerKind maps UPower device types to the page's icon kinds ("" = not shown).
func upowerKind(t uint32) string {
	switch t {
	case 2:
		return "laptop"
	case 5:
		return "mouse"
	case 6:
		return "keyboard"
	case 8, 10:
		return "phone"
	case 14:
		return "trackpad"
	case 12, 13:
		return "keyboard" // gamepad, pen: closest icon we have
	case 17, 19, 21:
		return "headphones"
	}
	return ""
}

// shortName: "Alex's Magic Mouse" → "Magic Mouse"; keeps labels short on a small screen.
func shortName(name string) string {
	f := strings.Fields(name)
	if len(f) > 1 && (strings.HasSuffix(f[0], "'s") || strings.HasSuffix(f[0], "’s") ||
		strings.HasSuffix(f[0], "'") || strings.HasSuffix(f[0], "’")) {
		return strings.Join(f[1:], " ")
	}
	return name
}

var logitechKeyboard = regexp.MustCompile(`(?i)\b(K\d{3}|MX Keys|Craft)\b`)

// refineKind fixes devices UPower files under the wrong type: Logitech's HID++ driver reports
// every receiver device as a mouse (a K400 keyboard included).
func refineKind(kind, name string) string {
	lower := strings.ToLower(name)
	switch {
	case strings.Contains(lower, "keyboard") || strings.Contains(lower, "teclado") || logitechKeyboard.MatchString(name):
		return "keyboard"
	case strings.Contains(lower, "trackpad") || strings.Contains(lower, "touchpad"):
		return "trackpad"
	}
	return kind
}
