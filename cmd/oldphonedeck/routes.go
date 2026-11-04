package main

import (
	"encoding/json"
	"net/http"
	"strings"
)

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func ok(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.Write([]byte("ok"))
}

// route handles the token-protected API. Same endpoints and JSON shapes as the Swift server.
func route(w http.ResponseWriter, r *http.Request, stats *Stats, music *Music) {
	p := r.Form
	switch r.Method + " " + r.URL.Path {
	case "GET /info":
		writeJSON(w, map[string]any{"camera": false})

	case "GET /stats":
		snap := stats.Snapshot()
		snap["music"] = music.Snapshot()
		writeJSON(w, snap)

	case "POST /speedtest":
		stats.RunSpeedTest()
		ok(w)

	case "POST /media":
		if cmd := p.Get("cmd"); cmd == "open" {
			go music.OpenPlayer()
		} else {
			music.Control(cmd)
		}
		ok(w)

	case "GET /artwork":
		art := music.Artwork()
		if art == nil {
			http.Error(w, "no artwork", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", http.DetectContentType(art))
		w.Write(art)

	case "GET /launcher":
		var selected any
		if s := LauncherSelected(); s != nil {
			selected = s
		}
		writeJSON(w, map[string]any{"selected": selected, "all": ListApps()})

	case "POST /launcher":
		// Body: paths=<path>\n<path>\n… in display order.
		var paths []string
		for _, s := range strings.Split(p.Get("paths"), "\n") {
			if s = strings.TrimSpace(s); s != "" {
				paths = append(paths, s)
			}
		}
		n, err := LauncherSave(paths)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"saved": n})

	case "GET /icon":
		path := p.Get("path")
		if !IsKnownApp(path) {
			http.Error(w, "unknown app", http.StatusNotFound)
			return
		}
		data, ctype := AppIcon(path)
		if data == nil {
			http.Error(w, "no icon", http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", ctype)
		w.Write(data)

	case "POST /launch":
		path := p.Get("path")
		if !IsKnownApp(path) {
			http.Error(w, "unknown app", http.StatusNotFound)
			return
		}
		if err := LaunchApp(path); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		ok(w)

	case "POST /claude":
		text := strings.TrimSpace(p.Get("text"))
		go ClaudeAsk(text)
		ok(w)

	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}
