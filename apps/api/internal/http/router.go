// Package http wires the REST API, the agent WebSocket endpoint and the
// renter terminal/tunnel endpoints.
package http

import (
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/cadencz/rentpc/apps/api/internal/auth"
	"github.com/cadencz/rentpc/apps/api/internal/config"
	"github.com/cadencz/rentpc/apps/api/internal/machines"
	"github.com/cadencz/rentpc/apps/api/internal/relay"
	"github.com/cadencz/rentpc/apps/api/internal/rentals"
)

type Server struct {
	Cfg      config.Config
	Auth     *auth.Service
	Machines *machines.Service
	Rentals  *rentals.Service
	Hub      *relay.Hub
}

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Use(middleware.RealIP, middleware.RequestID, middleware.Recoverer, requestTimeout(60*time.Second))
	r.Use(s.cors)
	r.Use(s.Auth.Middleware)

	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, 200, map[string]any{"ok": true, "time": time.Now().UTC()})
	})

	r.Route("/v1", func(r chi.Router) {
		// Auth
		r.Post("/auth/register", s.register)
		r.Post("/auth/login", s.login)
		r.Post("/auth/logout", s.logout)
		r.Get("/auth/me", s.me)
		r.With(auth.Require).Post("/auth/api-keys", s.createAPIKey)

		// Marketplace (public reads)
		r.Get("/market", s.market)
		r.Get("/market/{listingID}", s.marketItem)

		// Hosts (owner side)
		r.Group(func(r chi.Router) {
			r.Use(auth.Require)
			r.Get("/hosts", s.listHosts)
			r.Post("/hosts", s.createHost)
			r.Get("/hosts/{hostID}", s.getHost)
			r.Delete("/hosts/{hostID}", s.deleteHost)
			r.Post("/hosts/{hostID}/register-token", s.rotateRegisterToken)
			r.Put("/hosts/{hostID}/listing", s.upsertListing)
			r.Put("/hosts/{hostID}/location", s.setLocation)
			r.Get("/hosts/{hostID}/rentals", s.hostRentals)
			r.Get("/host/earnings", s.earnings)
			r.Get("/host/rentals", s.hostRentals)
		})

		// Rentals (renter side)
		r.Group(func(r chi.Router) {
			r.Use(auth.Require)
			r.Get("/rentals", s.myRentals)
			r.Post("/rentals", s.createRental)
			r.Get("/rentals/{rentalID}", s.getRental)
			r.Post("/rentals/{rentalID}/extend", s.extendRental)
			r.Post("/rentals/{rentalID}/terminate", s.terminateRental)
			r.Post("/rentals/{rentalID}/private-key", s.takePrivateKey)
			r.Get("/rentals/{rentalID}/events", s.rentalEvents)
			r.Get("/rentals/{rentalID}/terminal", s.terminalWS)
			r.Get("/rentals/{rentalID}/tunnel", s.tunnelWS)
		})

		// Agent
		r.Post("/agent/register", s.agentRegister)
		r.Get("/agent/connect", s.agentConnect)
	})
	return r
}

// ---- helpers -------------------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

func decode(r *http.Request, v any) error {
	r.Body = http.MaxBytesReader(nil, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(v)
}

// mapErr turns domain errors into HTTP status codes.
func mapErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, rentals.ErrNotFound), errors.Is(err, machines.ErrNotFound):
		writeErr(w, 404, err.Error())
	case errors.Is(err, rentals.ErrForbidden), errors.Is(err, machines.ErrForbidden):
		writeErr(w, 403, err.Error())
	case errors.Is(err, rentals.ErrNotAvailable), errors.Is(err, rentals.ErrBadState), errors.Is(err, rentals.ErrOwnMachine):
		writeErr(w, 409, err.Error())
	case errors.Is(err, auth.ErrInvalidCredentials):
		writeErr(w, 401, err.Error())
	default:
		slog.Error("request failed", "err", err)
		writeErr(w, 500, "internal error")
	}
}

func (s *Server) cors(next http.Handler) http.Handler {
	allowed := map[string]bool{}
	for _, o := range s.Cfg.WebOrigins {
		allowed[o] = true
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if origin != "" && (allowed[origin] || (s.Cfg.DevMode && strings.HasPrefix(origin, "http://localhost"))) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Access-Control-Allow-Credentials", "true")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
			w.Header().Set("Vary", "Origin")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// requestTimeout bounds ordinary requests but leaves long-lived connections
// (agent relay, renter terminal/tunnel WebSockets) alone.
func requestTimeout(d time.Duration) func(http.Handler) http.Handler {
	t := middleware.Timeout(d)
	return func(next http.Handler) http.Handler {
		timed := t(next)
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			p := r.URL.Path
			if strings.HasSuffix(p, "/terminal") || strings.HasSuffix(p, "/tunnel") || strings.HasSuffix(p, "/agent/connect") {
				next.ServeHTTP(w, r)
				return
			}
			timed.ServeHTTP(w, r)
		})
	}
}

func qInt(r *http.Request, k string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(k))
	return n
}
