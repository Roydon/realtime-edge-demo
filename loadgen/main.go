// Command loadgen drives the echo service with concurrent WebSocket sessions
// so the dashboards have real traffic to display.
//
// Without it, every panel in the stack is a flat line and the monitoring
// proves nothing. It also reports client-observed round-trip latency, which is
// the number that actually matters to a caller - the service's own histogram
// only sees its internal handling time, not the network and TLS edge in front
// of it.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"flag"
	"fmt"
	"math/rand/v2"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/coder/websocket"
)

type options struct {
	url         string
	connections int
	rate        float64
	duration    time.Duration
	ramp        time.Duration
	payload     int
	insecure    bool
}

func main() {
	var opt options
	flag.StringVar(&opt.url, "url", envOr("LOADGEN_URL", "wss://localhost:8443/ws"), "WebSocket URL to drive")
	flag.IntVar(&opt.connections, "connections", 25, "concurrent sessions to hold open")
	flag.Float64Var(&opt.rate, "rate", 5, "messages per second per session")
	flag.DurationVar(&opt.duration, "duration", 0, "how long to run; 0 means until interrupted")
	flag.DurationVar(&opt.ramp, "ramp", 5*time.Second, "spread session start-up over this window")
	flag.IntVar(&opt.payload, "payload", 256, "payload size in bytes")
	flag.BoolVar(&opt.insecure, "insecure", true, "skip TLS verification (the demo edge uses a self-signed cert)")
	flag.Parse()

	if opt.connections < 1 || opt.rate <= 0 {
		fmt.Fprintln(os.Stderr, "connections must be >= 1 and rate must be > 0")
		os.Exit(2)
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	if opt.duration > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, opt.duration)
		defer cancel()
	}

	fmt.Printf("loadgen -> %s | %d sessions | %.1f msg/s each | ramp %s\n",
		opt.url, opt.connections, opt.rate, opt.ramp)

	st := &stats{}
	go st.report(ctx)

	var wg sync.WaitGroup
	for i := 0; i < opt.connections; i++ {
		// Stagger session start-up. Opening every session in the same
		// millisecond produces a thundering herd that measures the accept
		// queue rather than steady-state latency.
		delay := time.Duration(float64(opt.ramp) * float64(i) / float64(opt.connections))
		wg.Add(1)
		go func(id int, delay time.Duration) {
			defer wg.Done()
			select {
			case <-time.After(delay):
			case <-ctx.Done():
				return
			}
			runSession(ctx, opt, st, id)
		}(i, delay)
	}

	wg.Wait()
	st.summary()
}

// runSession holds one session open, reconnecting with backoff if it drops.
// Reconnecting matters: the service closes sessions on injected errors and on
// drain, and a generator that gave up on the first close would under-report
// load exactly when the system is most interesting.
func runSession(ctx context.Context, opt options, st *stats, id int) {
	backoff := 250 * time.Millisecond
	for ctx.Err() == nil {
		err := oneSession(ctx, opt, st, id)
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			st.connErrors.Add(1)
		}
		select {
		case <-time.After(backoff + time.Duration(rand.Int64N(int64(backoff)))):
		case <-ctx.Done():
			return
		}
		if backoff < 5*time.Second {
			backoff *= 2
		}
	}
}

func oneSession(ctx context.Context, opt options, st *stats, id int) error {
	dialCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	client := &http.Client{}
	if opt.insecure {
		// The demo edge presents a self-signed certificate. Verification is
		// skipped here only because the generator is a test harness pointed at
		// a known local endpoint.
		client.Transport = &http.Transport{
			TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec // demo self-signed cert
		}
	}

	conn, _, err := websocket.Dial(dialCtx, opt.url, &websocket.DialOptions{HTTPClient: client})
	if err != nil {
		return fmt.Errorf("dial: %w", err)
	}
	defer conn.CloseNow()

	st.connected.Add(1)
	defer st.connected.Add(-1)

	payload := make([]byte, opt.payload)
	for i := range payload {
		payload[i] = byte('a' + (i+id)%26)
	}

	interval := time.Duration(float64(time.Second) / opt.rate)
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			_ = conn.Close(websocket.StatusNormalClosure, "run complete")
			return nil
		case <-ticker.C:
		}

		start := time.Now()
		msgCtx, msgCancel := context.WithTimeout(ctx, 15*time.Second)
		err := func() error {
			defer msgCancel()
			if err := conn.Write(msgCtx, websocket.MessageText, payload); err != nil {
				return fmt.Errorf("write: %w", err)
			}
			if _, _, err := conn.Read(msgCtx); err != nil {
				return fmt.Errorf("read: %w", err)
			}
			return nil
		}()
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, context.Canceled) {
				return nil
			}
			st.msgErrors.Add(1)
			return err
		}
		st.record(time.Since(start))
	}
}

// stats collects client-observed round-trip samples.
type stats struct {
	connected  atomic.Int64
	connErrors atomic.Int64
	msgErrors  atomic.Int64
	sent       atomic.Int64

	mu      sync.Mutex
	samples []time.Duration
}

func (s *stats) record(d time.Duration) {
	s.sent.Add(1)
	s.mu.Lock()
	defer s.mu.Unlock()
	// Reservoir-cap the sample set so a long run cannot grow memory without
	// bound; the percentiles stay representative.
	const maxSamples = 200_000
	if len(s.samples) < maxSamples {
		s.samples = append(s.samples, d)
		return
	}
	s.samples[rand.IntN(maxSamples)] = d
}

func (s *stats) snapshot() []time.Duration {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]time.Duration, len(s.samples))
	copy(out, s.samples)
	return out
}

func (s *stats) report(ctx context.Context) {
	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p := percentiles(s.snapshot())
			fmt.Printf("live: sessions=%d sent=%d p50=%s p95=%s p99=%s conn_err=%d msg_err=%d\n",
				s.connected.Load(), s.sent.Load(), p.p50, p.p95, p.p99,
				s.connErrors.Load(), s.msgErrors.Load())
		}
	}
}

func (s *stats) summary() {
	p := percentiles(s.snapshot())
	fmt.Println("--- loadgen summary ---")
	fmt.Printf("messages completed : %d\n", s.sent.Load())
	fmt.Printf("connection errors  : %d\n", s.connErrors.Load())
	fmt.Printf("message errors     : %d\n", s.msgErrors.Load())
	fmt.Printf("round-trip p50/p95/p99 : %s / %s / %s\n", p.p50, p.p95, p.p99)
}

type pct struct{ p50, p95, p99 time.Duration }

func percentiles(samples []time.Duration) pct {
	if len(samples) == 0 {
		return pct{}
	}
	sort.Slice(samples, func(i, j int) bool { return samples[i] < samples[j] })
	at := func(q float64) time.Duration {
		idx := int(q * float64(len(samples)-1))
		return samples[idx].Round(time.Microsecond)
	}
	return pct{p50: at(0.50), p95: at(0.95), p99: at(0.99)}
}

func envOr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}
