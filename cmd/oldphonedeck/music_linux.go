package main

import (
	"encoding/base64"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

// Music shows what's playing and controls it through MPRIS, the D-Bus interface every Linux
// player implements (Spotify, Firefox/Chrome tabs, Rhythmbox, VLC, mpv with a plugin…).
type Music struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	bus     string // D-Bus name of the player shown on the deck
	current map[string]any
	artID   string
	art     []byte
}

const (
	mprisPrefix = "org.mpris.MediaPlayer2."
	mprisPath   = "/org/mpris/MediaPlayer2"
	mprisPlayer = "org.mpris.MediaPlayer2.Player"
)

func NewMusic() *Music { return &Music{current: map[string]any{}} }

func (m *Music) Start() {
	conn, err := dbus.SessionBus()
	if err != nil {
		logf("Now playing disabled: no D-Bus session bus (%v)", err)
		return
	}
	m.conn = conn
	go func() {
		for {
			m.refresh()
			time.Sleep(2 * time.Second)
		}
	}()
}

func (m *Music) Snapshot() map[string]any {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make(map[string]any, len(m.current))
	for k, v := range m.current {
		out[k] = v
	}
	return out
}

func (m *Music) Artwork() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.art
}

func (m *Music) prop(bus, iface, name string) (dbus.Variant, error) {
	return m.conn.Object(bus, mprisPath).GetProperty(iface + "." + name)
}

// pickPlayer prefers whichever player is actually playing; if none is, a paused one.
func (m *Music) pickPlayer() (bus, status string) {
	var names []string
	if m.conn.BusObject().Call("org.freedesktop.DBus.ListNames", 0).Store(&names) != nil {
		return "", ""
	}
	var paused string
	for _, n := range names {
		if !strings.HasPrefix(n, mprisPrefix) {
			continue
		}
		v, err := m.prop(n, mprisPlayer, "PlaybackStatus")
		if err != nil {
			continue
		}
		switch s, _ := v.Value().(string); s {
		case "Playing":
			return n, s
		case "Paused":
			if paused == "" {
				paused = n
			}
		}
	}
	if paused != "" {
		return paused, "Paused"
	}
	return "", ""
}

func (m *Music) refresh() {
	bus, status := m.pickPlayer()
	if bus == "" {
		m.mu.Lock()
		m.bus, m.current = "", map[string]any{}
		m.mu.Unlock()
		return
	}
	mv, err := m.prop(bus, mprisPlayer, "Metadata")
	if err != nil {
		return
	}
	meta, _ := mv.Value().(map[string]dbus.Variant)
	str := func(k string) string {
		switch v := meta[k].Value().(type) {
		case string:
			return v
		case []string:
			return strings.Join(v, ", ")
		}
		return ""
	}
	micros := func(v any) float64 {
		switch n := v.(type) {
		case int64:
			return float64(n) / 1e6
		case uint64:
			return float64(n) / 1e6
		case int32:
			return float64(n) / 1e6
		case uint32:
			return float64(n) / 1e6
		case float64:
			return n / 1e6
		}
		return 0
	}
	title := str("xesam:title")
	if title == "" {
		m.mu.Lock()
		m.bus, m.current = bus, map[string]any{}
		m.mu.Unlock()
		return
	}
	artist, album, artURL := str("xesam:artist"), str("xesam:album"), str("mpris:artUrl")
	var position float64
	if pv, err := m.prop(bus, mprisPlayer, "Position"); err == nil {
		position = micros(pv.Value())
	}
	app := strings.TrimPrefix(bus, mprisPrefix)
	if iv, err := m.prop(bus, "org.mpris.MediaPlayer2", "Identity"); err == nil {
		if s, _ := iv.Value().(string); s != "" {
			app = s
		}
	}
	// Browsers report a placeholder track id, so derive a stable one from the track itself.
	h := fnv.New64a()
	fmt.Fprint(h, bus, "\x00", title, "\x00", artist, "\x00", album, "\x00", artURL)
	id := fmt.Sprintf("%x", h.Sum64())

	info := map[string]any{
		"app": app, "playing": status == "Playing", "title": title, "artist": artist, "album": album,
		"duration": micros(meta["mpris:length"].Value()), "position": position, "id": id,
	}
	m.mu.Lock()
	newTrack := id != m.artID
	if newTrack {
		m.artID, m.art = id, nil
	}
	info["hasArt"] = m.art != nil
	m.bus, m.current = bus, info
	m.mu.Unlock()
	if newTrack && artURL != "" {
		go m.fetchArtwork(id, artURL)
	}
}

func (m *Music) fetchArtwork(id, artURL string) {
	data := loadArt(artURL)
	if len(data) == 0 {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.artID == id {
		m.art = data
		if len(m.current) > 0 {
			m.current["hasArt"] = true
		}
	}
}

// loadArt reads mpris:artUrl, which may be file://, http(s):// or a data: URI.
func loadArt(raw string) []byte {
	if rest, ok := strings.CutPrefix(raw, "data:"); ok {
		if _, b64, found := strings.Cut(rest, ";base64,"); found {
			b, _ := base64.StdEncoding.DecodeString(b64)
			return b
		}
		return nil
	}
	u, err := url.Parse(raw)
	if err != nil {
		return nil
	}
	switch u.Scheme {
	case "file":
		b, _ := os.ReadFile(u.Path)
		return b
	case "http", "https":
		c := &http.Client{Timeout: 10 * time.Second}
		resp, err := c.Get(raw)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != 200 {
			return nil
		}
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
		return b
	}
	return nil
}

// Control: cmd is playpause | next | previous, sent to the player shown on the deck.
func (m *Music) Control(cmd string) {
	method := map[string]string{"playpause": "PlayPause", "next": "Next", "previous": "Previous"}[cmd]
	if method == "" || m.conn == nil {
		return
	}
	m.mu.Lock()
	bus := m.bus
	m.mu.Unlock()
	if bus == "" {
		bus, _ = m.pickPlayer()
	}
	if bus == "" {
		return
	}
	go func() {
		m.conn.Object(bus, mprisPath).Call(mprisPlayer+"."+method, 0)
		time.Sleep(250 * time.Millisecond) // let the player switch tracks before re-reading
		m.refresh()
	}()
}

// OpenPlayer brings the player's window to the front (if it supports Raise).
func (m *Music) OpenPlayer() {
	m.mu.Lock()
	bus := m.bus
	m.mu.Unlock()
	if bus == "" || m.conn == nil {
		return
	}
	m.conn.Object(bus, mprisPath).Call("org.mpris.MediaPlayer2.Raise", 0)
}
