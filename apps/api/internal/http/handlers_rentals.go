package http

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"

	"github.com/cadencz/rentpc/apps/api/internal/auth"
	"github.com/cadencz/rentpc/apps/api/internal/rentals"
	"github.com/cadencz/rentpc/packages/protocol"
)

func (s *Server) myRentals(w http.ResponseWriter, r *http.Request) {
	list, err := s.Rentals.ListForRenter(r.Context(), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rentals": list})
}

func (s *Server) createRental(w http.ResponseWriter, r *http.Request) {
	var in struct {
		ListingID string `json:"listing_id"`
		Hours     int    `json:"hours"`
	}
	if err := decode(r, &in); err != nil {
		writeErr(w, 400, "invalid json")
		return
	}
	if in.Hours == 0 {
		in.Hours = 1
	}
	rental, privKey, err := s.Rentals.Reserve(r.Context(), rentals.ReserveInput{
		RenterID: auth.CurrentUser(r.Context()).ID, ListingID: in.ListingID, Hours: in.Hours,
	})
	if err != nil {
		if strings.Contains(err.Error(), "hours must") {
			writeErr(w, 400, err.Error())
			return
		}
		mapErr(w, err)
		return
	}
	writeJSON(w, 201, map[string]any{"rental": rental, "ssh_private_key": privKey,
		"ssh_command": s.sshCommand(rental.ID)})
}

func (s *Server) sshCommand(rentalID string) string {
	return "ssh -i rentpc_" + rentalID[:8] + ".key -o ProxyCommand=\"rentpc tunnel --server " + s.Cfg.PublicURL + " " + rentalID + "\" renter@rentpc"
}

func (s *Server) getRental(w http.ResponseWriter, r *http.Request) {
	rental, err := s.Rentals.GetForUser(r.Context(), chi.URLParam(r, "rentalID"), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rental": rental, "ssh_command": s.sshCommand(rental.ID)})
}

func (s *Server) extendRental(w http.ResponseWriter, r *http.Request) {
	var in struct {
		Hours int `json:"hours"`
	}
	_ = decode(r, &in)
	if in.Hours == 0 {
		in.Hours = 1
	}
	rental, err := s.Rentals.Extend(r.Context(), chi.URLParam(r, "rentalID"), auth.CurrentUser(r.Context()).ID, in.Hours)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rental": rental})
}

func (s *Server) terminateRental(w http.ResponseWriter, r *http.Request) {
	rental, err := s.Rentals.Terminate(r.Context(), chi.URLParam(r, "rentalID"), auth.CurrentUser(r.Context()).ID, "terminated by user")
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"rental": rental})
}

func (s *Server) takePrivateKey(w http.ResponseWriter, r *http.Request) {
	key, err := s.Rentals.TakePrivateKey(r.Context(), chi.URLParam(r, "rentalID"), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	writeJSON(w, 200, map[string]any{"ssh_private_key": key})
}

func (s *Server) rentalEvents(w http.ResponseWriter, r *http.Request) {
	rental, err := s.Rentals.GetForUser(r.Context(), chi.URLParam(r, "rentalID"), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	rows, err := s.Rentals.DB.Query(r.Context(), `SELECT type, payload, at FROM rental_events WHERE rental_id=$1 ORDER BY at`, rental.ID)
	if err != nil {
		mapErr(w, err)
		return
	}
	defer rows.Close()
	type ev struct {
		Type    string          `json:"type"`
		Payload json.RawMessage `json:"payload"`
		At      time.Time       `json:"at"`
	}
	out := []ev{}
	for rows.Next() {
		var e ev
		if err := rows.Scan(&e.Type, &e.Payload, &e.At); err == nil {
			out = append(out, e)
		}
	}
	writeJSON(w, 200, map[string]any{"events": out})
}

// ---- Live connections -------------------------------------------------------------------

// openRentalStream authorises the caller and opens a stream to the sandbox.
func (s *Server) openRentalStream(w http.ResponseWriter, r *http.Request, kind protocol.StreamKind) (*rentals.Rental, net.Conn, bool) {
	rental, err := s.Rentals.GetForUser(r.Context(), chi.URLParam(r, "rentalID"), auth.CurrentUser(r.Context()).ID)
	if err != nil {
		mapErr(w, err)
		return nil, nil, false
	}
	if rental.RenterID != auth.CurrentUser(r.Context()).ID {
		writeErr(w, 403, "only the renter may connect")
		return nil, nil, false
	}
	if rental.Status != protocol.RentalReady && rental.Status != protocol.RentalActive && rental.Status != protocol.RentalExtending {
		writeErr(w, 409, "rental is "+string(rental.Status))
		return nil, nil, false
	}
	hdr := protocol.StreamHeader{RentalID: rental.ID, Kind: kind, Port: 22, Cols: qInt(r, "cols"), Rows: qInt(r, "rows")}
	st, err := s.Hub.OpenStream(r.Context(), rental.HostID, hdr)
	if err != nil {
		writeErr(w, 502, err.Error())
		return nil, nil, false
	}
	s.Rentals.Activate(context.Background(), rental.ID)
	return rental, st, true
}

func (s *Server) acceptWS(w http.ResponseWriter, r *http.Request) (*websocket.Conn, error) {
	patterns := []string{}
	for _, o := range s.Cfg.WebOrigins {
		patterns = append(patterns, strings.TrimPrefix(strings.TrimPrefix(o, "https://"), "http://"))
	}
	if s.Cfg.DevMode {
		patterns = append(patterns, "localhost:*")
	}
	return websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: patterns})
}

// terminalWS bridges a browser terminal (xterm.js) to a PTY inside the
// sandbox. Browser → server: binary frames = keystrokes, text frames =
// JSON control ({"type":"resize",...}). Server → browser: binary output.
func (s *Server) terminalWS(w http.ResponseWriter, r *http.Request) {
	_, st, ok := s.openRentalStream(w, r, protocol.StreamPTY)
	if !ok {
		return
	}
	ws, err := s.acceptWS(w, r)
	if err != nil {
		st.Close()
		return
	}
	defer ws.CloseNow()
	defer st.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()

	go func() {
		defer cancel()
		for {
			kind, payload, err := protocol.ReadFrame(st)
			if err != nil {
				return
			}
			if kind == protocol.FrameData {
				if err := ws.Write(ctx, websocket.MessageBinary, payload); err != nil {
					return
				}
			}
		}
	}()
	for {
		typ, data, err := ws.Read(ctx)
		if err != nil {
			return
		}
		if typ == websocket.MessageText {
			var msg protocol.PTYResize
			if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
				if err := protocol.WriteFrame(st, protocol.FrameResize, protocol.EncodeResize(msg.Cols, msg.Rows)); err != nil {
					return
				}
			}
			continue
		}
		if err := protocol.WriteFrame(st, protocol.FrameData, data); err != nil {
			return
		}
	}
}

// tunnelWS carries raw TCP (ssh) between the renter CLI and guest:22.
func (s *Server) tunnelWS(w http.ResponseWriter, r *http.Request) {
	_, st, ok := s.openRentalStream(w, r, protocol.StreamTCP)
	if !ok {
		return
	}
	ws, err := websocket.Accept(w, r, &websocket.AcceptOptions{OriginPatterns: []string{"*"}}) // CLI, not a browser
	if err != nil {
		st.Close()
		return
	}
	defer ws.CloseNow()
	defer st.Close()
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	nc := websocket.NetConn(ctx, ws, websocket.MessageBinary)
	go func() {
		defer cancel()
		_, _ = io.Copy(nc, st)
	}()
	_, err = io.Copy(st, nc)
	if err != nil && ctx.Err() == nil {
		slog.Debug("tunnel ended", "err", err)
	}
}
