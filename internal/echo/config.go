package echo

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// Config is the service's full runtime configuration. Everything is read from
// the environment: the process takes no config file, so the container image is
// identical across environments and only the compose/systemd unit differs.
type Config struct {
	ListenAddr     string
	MaxSessions    int
	IdleTimeout    time.Duration
	ShutdownGrace  time.Duration
	ReadLimitBytes int64

	// ProcessingJitter and ErrorInjectionRate exist purely so the demo stack
	// produces a latency distribution and an error rate worth putting on a
	// dashboard. A real service would have neither. They default to off.
	ProcessingJitter   time.Duration
	ErrorInjectionRate float64
}

// LoadConfig reads configuration from the environment, applying defaults. It
// returns an error rather than falling back to a default on malformed input:
// silently ignoring a typo'd limit is how a service ends up running with
// settings nobody intended.
func LoadConfig() (Config, error) {
	c := Config{
		ListenAddr:     envStr("LISTEN_ADDR", ":8080"),
		MaxSessions:    0,
		IdleTimeout:    0,
		ShutdownGrace:  0,
		ReadLimitBytes: 0,
	}

	var err error
	if c.MaxSessions, err = envInt("MAX_SESSIONS", 5000); err != nil {
		return c, err
	}
	if c.IdleTimeout, err = envDuration("IDLE_TIMEOUT", 75*time.Second); err != nil {
		return c, err
	}
	if c.ShutdownGrace, err = envDuration("SHUTDOWN_GRACE", 15*time.Second); err != nil {
		return c, err
	}
	if c.ReadLimitBytes, err = envInt64("READ_LIMIT_BYTES", 64*1024); err != nil {
		return c, err
	}
	if c.ProcessingJitter, err = envDuration("PROCESSING_JITTER", 0); err != nil {
		return c, err
	}
	if c.ErrorInjectionRate, err = envFloat("ERROR_INJECTION_RATE", 0); err != nil {
		return c, err
	}
	if c.ErrorInjectionRate < 0 || c.ErrorInjectionRate > 1 {
		return c, fmt.Errorf("ERROR_INJECTION_RATE must be between 0 and 1, got %v", c.ErrorInjectionRate)
	}
	return c, nil
}

func envStr(key, def string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return def
}

func envInt(key string, def int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envInt64(key string, def int64) (int64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return n, nil
}

func envFloat(key string, def float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return f, nil
}

func envDuration(key string, def time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%s: %w", key, err)
	}
	return d, nil
}
