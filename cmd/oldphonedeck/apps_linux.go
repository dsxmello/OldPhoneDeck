package main

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
)

// Linux apps are freedesktop .desktop entries (the same list GNOME's app grid shows).
// An App's Path is the .desktop file's absolute path.

// dataDirs returns $XDG_DATA_HOME followed by $XDG_DATA_DIRS, highest precedence first,
// plus the Flatpak/Snap export dirs in case the session didn't add them.
func dataDirs() []string {
	home, _ := os.UserHomeDir()
	dirs := []string{}
	if d := os.Getenv("XDG_DATA_HOME"); d != "" {
		dirs = append(dirs, d)
	} else {
		dirs = append(dirs, filepath.Join(home, ".local/share"))
	}
	sys := os.Getenv("XDG_DATA_DIRS")
	if sys == "" {
		sys = "/usr/local/share:/usr/share"
	}
	dirs = append(dirs, filepath.SplitList(sys)...)
	dirs = append(dirs, filepath.Join(home, ".local/share/flatpak/exports/share"),
		"/var/lib/flatpak/exports/share", "/var/lib/snapd/desktop")
	seen := map[string]bool{}
	out := []string{}
	for _, d := range dirs {
		if d != "" && !seen[d] {
			seen[d] = true
			out = append(out, d)
		}
	}
	return out
}

type desktopEntry struct{ name, icon string }

func currentDesktops() []string {
	return strings.Split(strings.ToUpper(os.Getenv("XDG_CURRENT_DESKTOP")), ":")
}

// localeKeys returns e.g. ["Name[pt_BR]", "Name[pt]", "Name"] for LANG=pt_BR.UTF-8.
func localeKeys(key string) []string {
	lang := os.Getenv("LC_MESSAGES")
	if lang == "" {
		lang = os.Getenv("LANG")
	}
	lang, _, _ = strings.Cut(lang, ".")
	lang, _, _ = strings.Cut(lang, "@")
	keys := []string{}
	if lang != "" && lang != "C" && lang != "POSIX" {
		keys = append(keys, key+"["+lang+"]")
		if short, _, found := strings.Cut(lang, "_"); found {
			keys = append(keys, key+"["+short+"]")
		}
	}
	return append(keys, key)
}

func parseDesktop(path string) (desktopEntry, bool) {
	f, err := os.Open(path)
	if err != nil {
		return desktopEntry{}, false
	}
	defer f.Close()
	kv := map[string]string{}
	inMain := false
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "[") {
			inMain = line == "[Desktop Entry]"
			continue
		}
		if !inMain || line == "" || line[0] == '#' {
			continue
		}
		if k, v, ok := strings.Cut(line, "="); ok {
			kv[strings.TrimSpace(k)] = strings.TrimSpace(v)
		}
	}
	if kv["Type"] != "Application" || kv["NoDisplay"] == "true" || kv["Hidden"] == "true" {
		return desktopEntry{}, false
	}
	desktops := currentDesktops()
	if only := kv["OnlyShowIn"]; only != "" && !listHasAny(only, desktops) {
		return desktopEntry{}, false
	}
	if not := kv["NotShowIn"]; not != "" && listHasAny(not, desktops) {
		return desktopEntry{}, false
	}
	if try := kv["TryExec"]; try != "" {
		if _, err := exec.LookPath(try); err != nil {
			return desktopEntry{}, false
		}
	}
	e := desktopEntry{icon: kv["Icon"]}
	for _, k := range localeKeys("Name") {
		if v := kv[k]; v != "" {
			e.name = v
			break
		}
	}
	return e, e.name != ""
}

func listHasAny(list string, want []string) bool {
	for _, item := range strings.Split(list, ";") {
		for _, w := range want {
			if item != "" && strings.EqualFold(item, w) {
				return true
			}
		}
	}
	return false
}

var (
	iconNamesMu sync.Mutex
	iconNames   = map[string]string{} // .desktop path → Icon= value
)

func listApps() []App {
	seenID := map[string]bool{}
	apps := []App{}
	icons := map[string]string{}
	for _, base := range dataDirs() {
		root := filepath.Join(base, "applications")
		filepath.WalkDir(root, func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".desktop") {
				return nil
			}
			// Desktop file ID: path relative to applications/, with "/" → "-". Earlier dirs win.
			rel, _ := filepath.Rel(root, path)
			id := strings.ReplaceAll(rel, string(filepath.Separator), "-")
			if seenID[id] {
				return nil
			}
			seenID[id] = true // a hidden entry in ~/.local still masks the system one
			e, ok := parseDesktop(path)
			if !ok {
				return nil
			}
			apps = append(apps, App{Name: e.name, Path: path})
			icons[path] = e.icon
			return nil
		})
	}
	sort.Slice(apps, func(i, j int) bool { return strings.ToLower(apps[i].Name) < strings.ToLower(apps[j].Name) })
	iconNamesMu.Lock()
	iconNames = icons
	iconNamesMu.Unlock()
	return apps
}

func LaunchApp(path string) error {
	cmd := exec.Command("gio", "launch", path)
	if err := cmd.Start(); err != nil {
		id := strings.TrimSuffix(filepath.Base(path), ".desktop")
		cmd = exec.Command("gtk-launch", id)
		if err := cmd.Start(); err != nil {
			return err
		}
	}
	go cmd.Wait()
	return nil
}

// ---- icons ----

const iconSize = 114 // what the page draws at 2x on a Retina phone

var (
	iconMu    sync.Mutex
	iconCache = map[string][]byte{}
)

// AppIcon returns PNG data for an app (SVG icons are rasterized with rsvg-convert when available).
func AppIcon(path string) ([]byte, string) {
	iconMu.Lock()
	if d, ok := iconCache[path]; ok {
		iconMu.Unlock()
		return d, "image/png"
	}
	iconMu.Unlock()

	iconNamesMu.Lock()
	name := iconNames[path]
	iconNamesMu.Unlock()

	file := findIcon(name)
	if file == "" {
		file = findIcon("application-x-executable")
	}
	if file == "" {
		return nil, ""
	}
	var data []byte
	if strings.HasSuffix(file, ".svg") || strings.HasSuffix(file, ".svgz") {
		out, err := exec.Command("rsvg-convert", "-w", strconv.Itoa(iconSize), "-h", strconv.Itoa(iconSize), "-a", file).Output()
		if err != nil {
			// No rasterizer: let the browser draw the SVG (every phone browser from iOS 5 / Android 3 can).
			b, err := os.ReadFile(file)
			if err != nil {
				return nil, ""
			}
			return b, "image/svg+xml"
		}
		data = out
	} else {
		b, err := os.ReadFile(file)
		if err != nil {
			return nil, ""
		}
		data = shrinkPNG(b, iconSize)
	}
	iconMu.Lock()
	iconCache[path] = data
	iconMu.Unlock()
	return data, "image/png"
}

var sizeDir = regexp.MustCompile(`^(\d+)x\d+(@(\d+))?$`)

// findIcon resolves an Icon= value: an absolute file, or a name looked up in the current
// icon theme, hicolor and pixmaps — preferring a bitmap near iconSize, then scalable SVG.
func findIcon(name string) string {
	if name == "" {
		return ""
	}
	if filepath.IsAbs(name) {
		if _, err := os.Stat(name); err == nil {
			return name
		}
		return ""
	}
	name = strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(name, ".png"), ".svg"), ".xpm")

	themes := []string{}
	if t := gnomeIconTheme(); t != "" {
		themes = append(themes, t)
	}
	themes = append(themes, "hicolor", "Adwaita")

	home, _ := os.UserHomeDir()
	bases := []string{filepath.Join(home, ".icons")}
	for _, d := range dataDirs() {
		bases = append(bases, filepath.Join(d, "icons"))
	}

	bestFile, bestScore := "", -1
	for _, theme := range themes {
		for _, base := range bases {
			matches, _ := filepath.Glob(filepath.Join(base, theme, "*", "*", name+".*"))
			for _, m := range matches {
				if s := iconScore(m); s > bestScore {
					bestFile, bestScore = m, s
				}
			}
		}
		if bestFile != "" {
			return bestFile // a hit in a higher-priority theme beats a "better" size elsewhere
		}
	}
	for _, d := range dataDirs() {
		for _, ext := range []string{".png", ".svg"} {
			p := filepath.Join(d, "pixmaps", name+ext)
			if _, err := os.Stat(p); err == nil {
				return p
			}
		}
	}
	return ""
}

// iconScore ranks candidate files: bitmaps sized at or above iconSize first (closest wins),
// then scalable SVGs, then smaller bitmaps.
func iconScore(path string) int {
	ext := filepath.Ext(path)
	if ext != ".png" && ext != ".svg" {
		return -1
	}
	sizePart := filepath.Base(filepath.Dir(filepath.Dir(path))) // e.g. "128x128", "scalable"
	if strings.Contains(path, "symbolic") {
		return 0
	}
	if ext == ".svg" {
		return 500
	}
	m := sizeDir.FindStringSubmatch(sizePart)
	if m == nil {
		return 10
	}
	size, _ := strconv.Atoi(m[1])
	if m[3] != "" {
		scale, _ := strconv.Atoi(m[3])
		size *= scale
	}
	if size >= iconSize {
		return 1000 - (size - iconSize) // 128 beats 256 beats 512
	}
	return size // 96 < svg
}

var (
	themeOnce sync.Once
	themeName string
)

func gnomeIconTheme() string {
	themeOnce.Do(func() {
		out, err := exec.Command("gsettings", "get", "org.gnome.desktop.interface", "icon-theme").Output()
		if err == nil {
			themeName = strings.Trim(strings.TrimSpace(string(out)), "'")
		}
	})
	return themeName
}
