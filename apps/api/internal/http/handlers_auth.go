package http

import (
	"net/http"
	"time"

	"github.com/cadencz/rentpc/apps/api/internal/auth"
)

type credentials struct {
	Email       string `json:"email"`
	Password    string `json:"password"`
	DisplayName string `json:"display_name"`
}

func (s *Server) setSessionCookie(w http.ResponseWriter, token string, exp time.Time) {
	http.SetCookie(w, &http.Cookie{
		Name: auth.CookieName, Value: token, Path: "/", Expires: exp,
		HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: s.sameSite(),
	})
}

// sameSite: the web app (Vercel) and the API (Render) live on different
// sites in production, so the cookie must be SameSite=None; Secure. Locally
// (both on localhost) Lax is fine and works over plain http.
func (s *Server) sameSite() http.SameSite {
	if s.Cfg.CookieSecure {
		return http.SameSiteNoneMode
	}
	return http.SameSiteLaxMode
}

func (s *Server) register(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	u, err := s.Auth.Register(r.Context(), in.Email, in.Password, in.DisplayName)
	if err != nil {
		writeErr(w, 400, err.Error())
		return
	}
	tok, exp, err := s.Auth.CreateSession(r.Context(), u.ID, r.UserAgent(), r.RemoteAddr)
	if err != nil {
		mapErr(w, err)
		return
	}
	s.setSessionCookie(w, tok, exp)
	writeJSON(w, 201, map[string]any{"user": u})
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var in credentials
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	u, err := s.Auth.Authenticate(r.Context(), in.Email, in.Password)
	if err != nil {
		mapErr(w, err)
		return
	}
	tok, exp, err := s.Auth.CreateSession(r.Context(), u.ID, r.UserAgent(), r.RemoteAddr)
	if err != nil {
		mapErr(w, err)
		return
	}
	s.setSessionCookie(w, tok, exp)
	writeJSON(w, 200, map[string]any{"user": u})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(auth.CookieName); err == nil {
		_ = s.Auth.DeleteSession(r.Context(), c.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: auth.CookieName, Value: "", Path: "/", MaxAge: -1, HttpOnly: true, Secure: s.Cfg.CookieSecure, SameSite: s.sameSite()})
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	u := auth.CurrentUser(r.Context())
	if u == nil {
		writeJSON(w, 200, map[string]any{"user": nil})
		return
	}
	writeJSON(w, 200, map[string]any{"user": u})
}

func (s *Server) createAPIKey(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Label string `json:"label"`
	}
	_ = decode(r, &in)
	if in.Label == "" {
		in.Label = "default"
	}
	key, err := s.Auth.CreateAPIKey(r.Context(), auth.CurrentUser(r.Context()).ID, in.Label)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"api_key": key, "label": in.Label,
		"usage": "Authorization: Bearer " + key})
}
