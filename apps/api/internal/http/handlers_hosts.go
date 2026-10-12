package http

import (
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/cadencz/rentpc/apps/api/internal/auth"
	"github.com/cadencz/rentpc/apps/api/internal/machines"
	"github.com/cadencz/rentpc/apps/api/internal/payments"
)

func (s *Server) listHosts(w http.ResponseWriter, r *http.Request) {
	hosts, err := s.Machines.HostsForUser(r.Context(), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"hosts": hosts})
}

func (s *Server) createHost(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Name     string `json:"name"`
		Platform string `json:"platform"`
	}
	_ = decode(r, &in)
	if in.Platform == "" {
		in.Platform = "windows"
	}
	h, tok, err := s.Machines.CreateHost(r.Context(), auth.CurrentUser(r.Context()).ID, in.Name, in.Platform)
	if err != nil {
		if strings.Contains(err.Error(), "platform must") {
			writeErr(w, 400, err.Error())
			return
		}
		mapErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"host": h, "register_token": tok, "register_command": s.registerCommand(tok), "server": s.Cfg.PublicURL})
}

func (s *Server) registerCommand(tok string) string {
	return "rentpc-host register --server " + s.Cfg.PublicURL + " --token " + tok
}

func (s *Server) getHost(w http.ResponseWriter, r *http.Request) {
	h, err := s.Machines.GetHost(r.Context(), chi.URLParam(r, "hostID"))
	if err != nil {
		mapErr(w, err)
		return
	}
	if h.UserID != auth.CurrentUser(r.Context()).ID {
		writeErr(w, 403, "forbidden")
		return
	}
	writeJSON(w, 200, map[string]any{"host": h})
}

func (s *Server) deleteHost(w http.ResponseWriter, r *http.Request) {
	if err := s.Machines.DeleteHost(r.Context(), chi.URLParam(r, "hostID"), auth.CurrentUser(r.Context()).ID); err != nil {
		writeErr(w, 409, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) rotateRegisterToken(w http.ResponseWriter, r *http.Request) {
	tok, err := s.Machines.RotateRegisterToken(r.Context(), chi.URLParam(r, "hostID"), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"register_token": tok, "register_command": s.registerCommand(tok), "server": s.Cfg.PublicURL})
}

func (s *Server) upsertListing(w http.ResponseWriter, r *http.Request) {
	var in machines.ListingInput
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	l, err := s.Machines.UpsertListing(r.Context(), chi.URLParam(r, "hostID"), auth.CurrentUser(r.Context()).ID, in)
	if err != nil {
		if strings.Contains(err.Error(), "must be") || strings.Contains(err.Error(), "not available") || strings.Contains(err.Error(), "has not reported") {
			writeErr(w, 400, err.Error())
			return
		}
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"listing": l})
}

func (s *Server) setLocation(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Country string `json:"country"`
		Region  string `json:"region"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if err := s.Machines.SetMachineLocation(r.Context(), chi.URLParam(r, "hostID"), auth.CurrentUser(r.Context()).ID, strings.ToUpper(in.Country), in.Region); err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ok": true})
}

func (s *Server) hostRentals(w http.ResponseWriter, r *http.Request) {
	list, err := s.Rentals.ListForHostUser(r.Context(), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rentals": list})
}

func (s *Server) earnings(w http.ResponseWriter, r *http.Request) {
	e, err := s.Machines.EarningsForUser(r.Context(), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"earnings": e})
}

// ---- Marketplace ------------------------------------------------------------------

func (s *Server) market(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	f := machines.SearchFilter{
		Query:      q.Get("q"),
		OSFamily:   q.Get("os"),
		GPUVendor:  q.Get("gpu"),
		MinCPUs:    qInt(r, "min_cpus"),
		MinRAMMB:   int64(qInt(r, "min_ram_mb")),
		MinVRAMMB:  int64(qInt(r, "min_vram_mb")),
		MaxPrice:   qInt(r, "max_price_cents"),
		Country:    strings.ToUpper(q.Get("country")),
		OnlineOnly: q.Get("online") == "1" || q.Get("online") == "true",
		Sort:       q.Get("sort"),
		Limit:      qInt(r, "limit"),
		Offset:     qInt(r, "offset"),
	}
	items, err := s.Machines.Search(r.Context(), f)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"items": items})
}

func (s *Server) marketItem(w http.ResponseWriter, r *http.Request) {
	it, err := s.Machines.MarketItemByListing(r.Context(), chi.URLParam(r, "listingID"))
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"item": it})
}

// quote returns the price of renting a listing for N hours; used by the
// payment gateway to build the MPP challenge, and by clients to show totals.
func (s *Server) quote(w http.ResponseWriter, r *http.Request) {
	it, err := s.Machines.MarketItemByListing(r.Context(), chi.URLParam(r, "listingID"))
	if err != nil {
		mapErr(w, err)
		return
	}
	hours := qInt(r, "hours")
	if hours < 1 {
		hours = 1
	}
	if hours < it.Listing.MinHours || hours > it.Listing.MaxHours {
		writeErr(w, 400, "hours out of range for this listing")
		return
	}
	total := int64(hours) * int64(it.Listing.PriceCentsPerHour)
	payout, _ := s.Auth.PayoutAddressForHost(r.Context(), it.HostID)
	fee := payments.Fee(payments.Money{AmountCents: total}).AmountCents
	writeJSON(w, 200, map[string]any{
		"listing_id": it.Listing.ID, "hours": hours, "price_cents_per_hour": it.Listing.PriceCentsPerHour,
		"total_cents": total, "currency": it.Listing.Currency, "available": it.Available,
		"host_id": it.HostID, "title": it.Listing.Title,
		// On-chain split: the gateway routes total-fee to the host's wallet when set.
		"platform_fee_bps": payments.PlatformFeeBps, "platform_fee_cents": fee,
		"host_payout_cents": total - fee, "host_payout_address": payout,
	})
}

// ---- Agent registration ---------------------------------------------------------------

func (s *Server) agentRegister(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Token string `json:"token"`
	}
	if err := decode(r, &in); err != nil || in.Token == "" {
		writeErr(w, 400, "token required")
		return
	}
	hostID, agentTok, err := s.Machines.RedeemRegisterToken(r.Context(), in.Token)
	if err != nil {
		writeErr(w, 401, err.Error())
		return
	}
	writeJSON(w, 200, map[string]any{"host_id": hostID, "agent_token": agentTok})
}

func (s *Server) agentConnect(w http.ResponseWriter, r *http.Request) {
	tok := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tok == "" {
		tok = r.URL.Query().Get("token")
	}
	hostID, err := s.Machines.HostByAgentToken(r.Context(), tok)
	if err != nil {
		writeErr(w, 401, err.Error())
		return
	}
	s.Hub.ServeAgent(w, r, hostID)
}
