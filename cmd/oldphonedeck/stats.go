package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

func logf(format string, args ...any) { fmt.Fprintf(os.Stderr, format+"\n", args...) }

// Battery is one device shown in the Batteries widget.
// Kind: laptop | keyboard | mouse | trackpad | headphones | phone.
type Battery struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	Level    int    `json:"level"`
	Charging bool   `json:"charging"`
	Case     *int   `json:"case,omitempty"`
}

// Stats feeds the dashboard widgets: live network throughput, speed tests and batteries.
type Stats struct {
	mu        sync.Mutex
	lastRx    uint64
	lastTx    uint64
	lastAt    time.Time
	history   [][2]float64 // [rxMbps, txMbps], one per second, newest last
	speed     map[string]any
	testing   bool
	batteries []Battery
}

func NewStats() *Stats { return &Stats{speed: map[string]any{}, batteries: []Battery{}} }

func (s *Stats) Start() {
	go func() {
		for range time.Tick(time.Second) {
			s.sampleNetwork()
		}
	}()
	go func() {
		for {
			b := readBatteries()
			s.mu.Lock()
			s.batteries = b
			s.mu.Unlock()
			time.Sleep(30 * time.Second)
		}
	}()
	// First speed test shortly after launch so the widget isn't empty.
	time.AfterFunc(5*time.Second, s.RunSpeedTest)
}

func (s *Stats) Snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	speed := map[string]any{"running": s.testing}
	for k, v := range s.speed {
		speed[k] = v
	}
	hist := make([]float64, len(s.history))
	var rx, tx float64
	for i, h := range s.history {
		hist[i] = h[0]
	}
	if n := len(s.history); n > 0 {
		rx, tx = s.history[n-1][0], s.history[n-1][1]
	}
	return map[string]any{
		"net":       map[string]any{"rx": rx, "tx": tx, "history": hist},
		"speed":     speed,
		"batteries": s.batteries,
		"mac":       hostName(), // key name kept for compatibility with the page
	}
}

func (s *Stats) sampleNetwork() {
	rx, tx, ok := readNetBytes()
	if !ok {
		return
	}
	now := time.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.lastAt.IsZero() {
		dt := now.Sub(s.lastAt).Seconds()
		var drx, dtx float64
		if rx >= s.lastRx { // counters can reset (interface down/up); skip negative deltas
			drx = float64(rx - s.lastRx)
		}
		if tx >= s.lastTx {
			dtx = float64(tx - s.lastTx)
		}
		s.history = append(s.history, [2]float64{drx * 8 / dt / 1e6, dtx * 8 / dt / 1e6})
		if len(s.history) > 60 {
			s.history = s.history[len(s.history)-60:]
		}
	}
	s.lastRx, s.lastTx, s.lastAt = rx, tx, now
}

// RunSpeedTest measures ping, download and upload against Cloudflare's public speed test endpoints
// (the same ones speed.cloudflare.com uses), so it works the same on every OS.
func (s *Stats) RunSpeedTest() {
	s.mu.Lock()
	if s.testing {
		s.mu.Unlock()
		return
	}
	s.testing = true
	s.mu.Unlock()

	go func() {
		result := speedTest()
		s.mu.Lock()
		s.testing = false
		if len(result) > 0 {
			s.speed = result
		}
		s.mu.Unlock()
	}()
}

const speedHost = "https://speed.cloudflare.com"

func speedTest() map[string]any {
	client := &http.Client{Timeout: 20 * time.Second}
	out := map[string]any{}

	// Ping: best of a few tiny requests over a warm connection.
	best := time.Duration(0)
	for i := 0; i < 6; i++ {
		t0 := time.Now()
		resp, err := client.Get(speedHost + "/__down?bytes=0")
		if err != nil {
			continue
		}
		io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if d := time.Since(t0); i > 0 && (best == 0 || d < best) { // i=0 includes TCP+TLS setup
			best = d
		}
	}
	if best == 0 {
		return nil // offline
	}
	out["ping"] = float64(best.Microseconds()) / 1000

	if mbps := parallelTransfer(4, 8*time.Second, func(ctx context.Context) (int64, error) {
		req, _ := http.NewRequestWithContext(ctx, "GET", speedHost+"/__down?bytes=50000000", nil)
		resp, err := client.Do(req)
		if err != nil {
			return 0, err
		}
		defer resp.Body.Close()
		return io.Copy(io.Discard, resp.Body)
	}); mbps > 0 {
		out["down"] = mbps
	}

	if mbps := parallelTransfer(4, 8*time.Second, func(ctx context.Context) (int64, error) {
		body := &countingReader{remaining: 20_000_000}
		req, _ := http.NewRequestWithContext(ctx, "POST", speedHost+"/__up", body)
		req.ContentLength = body.remaining
		resp, err := client.Do(req)
		if resp != nil {
			resp.Body.Close()
		}
		return body.sent.Load(), err
	}); mbps > 0 {
		out["up"] = mbps
	}
	out["at"] = float64(time.Now().UnixMilli())
	return out
}

// parallelTransfer runs n workers that each repeat fn until the time budget is spent; returns Mbps.
func parallelTransfer(n int, budget time.Duration, fn func(context.Context) (int64, error)) float64 {
	ctx, cancel := context.WithTimeout(context.Background(), budget)
	defer cancel()
	var mu sync.Mutex
	var total int64
	var wg sync.WaitGroup
	t0 := time.Now()
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				b, err := fn(ctx)
				mu.Lock()
				total += b
				mu.Unlock()
				if err != nil && ctx.Err() == nil {
					return
				}
			}
		}()
	}
	wg.Wait()
	secs := time.Since(t0).Seconds()
	if secs <= 0 || total == 0 {
		return 0
	}
	return float64(total) * 8 / secs / 1e6
}

// countingReader produces zero bytes for an upload and counts how many were actually read.
type countingReader struct {
	remaining int64
	sent      atomic.Int64 // read by the worker while the transport may still be reading
}

func (r *countingReader) Read(p []byte) (int, error) {
	if r.remaining <= 0 {
		return 0, io.EOF
	}
	if int64(len(p)) > r.remaining {
		p = p[:r.remaining]
	}
	clear(p)
	r.remaining -= int64(len(p))
	r.sent.Add(int64(len(p)))
	return len(p), nil
}
