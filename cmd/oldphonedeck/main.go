// Command oldphonedeck turns an old phone or tablet into a Stream Deck–style control panel
// for your computer. Cross-platform Go port of the original Swift (macOS) server: the HTTP API
// is the same, so the phone page (index.html) works unchanged.
package main

import (
	"crypto/rand"
	"errors"
	"flag"
	"fmt"
	"math/big"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/dsxmello/OldPhoneDeck/Sources/oldphonedeck/web"
)

var configDir = func() string {
	home, err := os.UserHomeDir()
	if err != nil {
		home = "."
	}
	return filepath.Join(home, ".oldphonedeck")
}()

// loadToken returns the access code, creating one on first run.
// Anyone on the Wi-Fi could otherwise open apps on this computer.
func loadToken() string {
	file := filepath.Join(configDir, "token")
	if b, err := os.ReadFile(file); err == nil {
		if t := strings.TrimSpace(string(b)); t != "" {
			return t
		}
	}
	const alphabet = "abcdefghjkmnpqrstuvwxyz23456789"
	var sb strings.Builder
	for i := 0; i < 12; i++ {
		n, _ := rand.Int(rand.Reader, big.NewInt(int64(len(alphabet))))
		sb.WriteByte(alphabet[n.Int64()])
	}
	t := sb.String()
	_ = os.MkdirAll(configDir, 0o700)
	_ = os.WriteFile(file, []byte(t), 0o600)
	return t
}

// lanAddress finds the IPv4 address other devices on the network can reach.
func lanAddress() string {
	// UDP "dial" sends nothing; it just asks the kernel which interface routes outward.
	if c, err := net.Dial("udp4", "192.0.2.1:9"); err == nil {
		defer c.Close()
		if a, ok := c.LocalAddr().(*net.UDPAddr); ok && !a.IP.IsLoopback() {
			return a.IP.String()
		}
	}
	ifaces, _ := net.Interfaces()
	for _, i := range ifaces {
		if i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 {
			continue
		}
		addrs, _ := i.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil {
				return n.IP.String()
			}
		}
	}
	return "localhost"
}

func main() {
	port := flag.Int("port", 8045, "HTTP port (port 80 is used too when the system allows it)")
	name := flag.String("name", "oldphonedeck", `address on the network: <name>.local ("" to turn mDNS off)`)
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "oldphonedeck: turn an old phone or tablet into a Stream Deck-style control panel for your computer")
		flag.PrintDefaults()
	}
	flag.Parse()

	token := loadToken()
	stats := NewStats()
	stats.Start()
	music := NewMusic()
	music.Start()

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		// Public: the page shell, so a home-screen bookmark loads before it knows the token.
		if r.URL.Path == "/" || r.URL.Path == "/index.html" {
			w.Header().Set("Content-Type", "text/html; charset=utf-8")
			w.Write(web.IndexHTML)
			return
		}
		if r.URL.Query().Get("k") != token {
			http.Error(w, "bad token", http.StatusForbidden)
			return
		}
		_ = r.ParseForm() // merges the query string with an x-www-form-urlencoded body
		route(w, r, stats, music)
	})

	ln, err := net.Listen("tcp", fmt.Sprintf(":%d", *port))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Cannot listen on %d: %v\n", *port, err)
		os.Exit(1)
	}
	srv := &http.Server{Handler: noStore(mux), ReadHeaderTimeout: 10 * time.Second}
	go serve(srv, ln)

	// Also serve on port 80 so the address needs no ":8045". Linux only allows that for root unless
	// net.ipv4.ip_unprivileged_port_start is lowered; Windows and macOS allow it by default.
	webPort := *port
	if *port != 80 {
		if ln80, err := net.Listen("tcp", ":80"); err == nil {
			webPort = 80
			go serve(srv, ln80)
		} else {
			port80Hint(err)
		}
	}

	ip := lanAddress()
	host := ip
	*name = strings.ToLower(strings.TrimSuffix(strings.TrimSpace(*name), ".local"))
	if *name != "" {
		if err := startMDNS(*name, func() net.IP { return net.ParseIP(lanAddress()) }); err != nil {
			logf("⚠️  Could not publish %s.local (mDNS): %v — use the IP address instead.", *name, err)
		} else {
			host = *name + ".local"
		}
	}
	link := func(h string) string {
		if webPort == 80 {
			return "http://" + h
		}
		return fmt.Sprintf("http://%s:%d", h, webPort)
	}
	fmt.Printf(`
OldPhoneDeck is running. On the phone or tablet (same Wi-Fi), open the browser and go to:

    %s

and enter the access code:  %s   (asked once; the browser remembers it)
`, link(host), groupCode(token))
	if host != ip {
		fmt.Printf(`
If that name doesn't open (Android 11 and older can't resolve .local names), use:  %s
`, link(ip))
	}
	fmt.Print(`
Then add it to the home screen (Safari: Share → Add to Home Screen; Chrome: ⋮ → Add to Home screen).
Hold any key to choose your apps. Ctrl-C to quit.

`)
	select {}
}

func serve(srv *http.Server, ln net.Listener) {
	if err := srv.Serve(ln); err != nil {
		fmt.Fprintln(os.Stderr, "Server failed:", err)
		os.Exit(1)
	}
}

// groupCode shows "wbhj6bbk54bm" as "wbhj-6bbk-54bm" so it's easy to read off the screen and type.
func groupCode(t string) string {
	var parts []string
	for len(t) > 4 {
		parts = append(parts, t[:4])
		t = t[4:]
	}
	return strings.Join(append(parts, t), "-")
}

func port80Hint(err error) {
	if errors.Is(err, syscall.EACCES) {
		logf(`ℹ️  Port 80 needs permission, so the address will include the port. To drop it, allow it once with:
    echo 'net.ipv4.ip_unprivileged_port_start=80' | sudo tee /etc/sysctl.d/60-oldphonedeck.conf && sudo sysctl --system`)
	} else if errors.Is(err, syscall.EADDRINUSE) {
		logf("ℹ️  Port 80 is used by another program, so the address will include the port.")
	} else {
		logf("ℹ️  Can't use port 80 (%v), so the address will include the port.", err)
	}
}

func noStore(h http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		h.ServeHTTP(w, r)
	})
}
