// Package config loads backend configuration from the environment.
package config

import (
	"bufio"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	Addr         string   // listen address, e.g. ":8080"
	DatabaseURL  string   // postgres://...
	RedisURL     string   // optional; presence falls back to in-memory
	PublicURL    string   // https://api.rentpc.example — used in agent setup instructions
	WebOrigins   []string // allowed CORS origins (Vercel app, localhost)
	CookieSecure bool     // Secure flag on the session cookie
	SessionTTL   time.Duration
	// ReservationTTL is how long a reservation may sit without the agent
	// acknowledging provisioning before it expires.
	ReservationTTL time.Duration
	// ProvisionTimeout is how long provisioning may take before the rental fails.
	ProvisionTimeout time.Duration
	// HeartbeatGrace is how long a host may be silent before it is marked offline.
	HeartbeatGrace time.Duration
	// MinAgentProtocol rejects agents older than this.
	MinAgentProtocol int
	// DevMode relaxes a few things (plain-text logging, seed data allowed).
	DevMode bool
}

func Load() (Config, error) {
	// Load a .env file from the working directory (or ENV_FILE) for local
	// development. Real environment variables always take precedence.
	loadDotEnv(env("ENV_FILE", ".env"))

	c := Config{
		Addr:             env("ADDR", ":"+env("PORT", "8080")),
		DatabaseURL:      os.Getenv("DATABASE_URL"),
		RedisURL:         os.Getenv("REDIS_URL"),
		PublicURL:        env("PUBLIC_URL", "http://localhost:8080"),
		WebOrigins:       split(env("WEB_ORIGINS", "http://localhost:3000")),
		CookieSecure:     envBool("COOKIE_SECURE", false),
		SessionTTL:       envDur("SESSION_TTL", 7*24*time.Hour),
		ReservationTTL:   envDur("RESERVATION_TTL", 5*time.Minute),
		ProvisionTimeout: envDur("PROVISION_TIMEOUT", 10*time.Minute),
		HeartbeatGrace:   envDur("HEARTBEAT_GRACE", 90*time.Second),
		MinAgentProtocol: envInt("MIN_AGENT_PROTOCOL", 1),
		DevMode:          envBool("DEV_MODE", false),
	}
	if c.DatabaseURL == "" {
		return c, fmt.Errorf("DATABASE_URL is required")
	}
	return c, nil
}

// loadDotEnv reads KEY=VALUE lines from path and sets any that are not
// already present in the environment. Missing file is not an error.
func loadDotEnv(path string) {
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		}
		if k != "" && os.Getenv(k) == "" {
			os.Setenv(k, v)
		}
	}
}

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func envBool(k string, def bool) bool {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return def
	}
	return b
}

func envInt(k string, def int) int {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

func envDur(k string, def time.Duration) time.Duration {
	v := os.Getenv(k)
	if v == "" {
		return def
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return def
	}
	return d
}

func split(s string) []string {
	var out []string
	for _, p := range strings.Split(s, ",") {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}
