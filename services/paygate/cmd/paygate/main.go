// paygate: MPP (Machine Payments Protocol) gateway for rentPC. See README.md.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/server"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/server"

	"github.com/cadencz/rentpc/services/paygate/internal/claim"
)

func env(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

type gateway struct {
	api     *url.URL
	proxy   *httputil.ReverseProxy
	payment *server.Mpp
	realm   string
	secret  string
	client  *http.Client
}

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	apiURL, err := url.Parse(env("API_URL", "http://api:8080"))
	if err != nil {
		slog.Error("API_URL", "err", err)
		os.Exit(1)
	}
	secret := os.Getenv("PAYGATE_SECRET")
	mppSecret := os.Getenv("MPP_SECRET_KEY")
	recipient := os.Getenv("MPP_RECIPIENT")
	realm := env("MPP_REALM", apiURL.Host)
	if secret == "" || len(mppSecret) < 32 || recipient == "" {
		slog.Error("PAYGATE_SECRET, MPP_SECRET_KEY (>=32 bytes) and MPP_RECIPIENT are required")
		os.Exit(1)
	}

	method, err := charge.MethodFromConfig(charge.Config{
		Store:      tempo.NewMemoryStore(), // single instance; use the Redis store when scaling out
		RPCURL:     env("TEMPO_RPC_URL", "https://rpc.moderato.tempo.xyz"),
		Currencies: []string{env("MPP_CURRENCY", tempo.OUSDAddress)},
		Recipient:  recipient,
	})
	if err != nil {
		slog.Error("tempo method", "err", err)
		os.Exit(1)
	}
	// Our clients already use Authorization for their API key, so the MPP
	// credential travels in Payment-Authorization.
	payment, err := server.New(method, realm, mppSecret, server.WithRequiresAuth(true))
	if err != nil {
		slog.Error("mpp server", "err", err)
		os.Exit(1)
	}

	g := &gateway{api: apiURL, proxy: httputil.NewSingleHostReverseProxy(apiURL), payment: payment, realm: realm, secret: secret,
		client: &http.Client{Timeout: 15 * time.Second}}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"ok":true,"service":"paygate"}`) })
	mux.HandleFunc("POST /v1/rentals", g.rent)
	mux.Handle("/", g.proxy)

	addr := env("LISTEN", ":8090")
	slog.Info("paygate listening", "addr", addr, "api", apiURL.String(), "realm", realm, "recipient", recipient)
	if err := http.ListenAndServe(addr, mux); err != nil {
		slog.Error("listen", "err", err)
		os.Exit(1)
	}
}

type rentBody struct {
	ListingID string `json:"listing_id"`
	Hours     int    `json:"hours"`
}

type quote struct {
	TotalCents int64  `json:"total_cents"`
	Currency   string `json:"currency"`
	Title      string `json:"title"`
	Available  bool   `json:"available"`
}

func (g *gateway) rent(w http.ResponseWriter, r *http.Request) {
	// Metered: API-key clients (agents) and browser sessions that opted in
	// with X-RentPC-Pay: mpp (the "Pay with Tempo wallet" button). Plain
	// browser rentals keep the mock provider.
	if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer rpk_") && !strings.EqualFold(r.Header.Get("X-RentPC-Pay"), "mpp") {
		g.proxy.ServeHTTP(w, r)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		http.Error(w, "bad body", http.StatusBadRequest)
		return
	}
	var in rentBody
	if err := json.Unmarshal(body, &in); err != nil || in.ListingID == "" {
		http.Error(w, `{"error":"listing_id required"}`, http.StatusBadRequest)
		return
	}
	if in.Hours < 1 {
		in.Hours = 1
	}
	q, err := g.quote(r.Context(), in.ListingID, in.Hours)
	if err != nil {
		http.Error(w, `{"error":"`+err.Error()+`"}`, http.StatusBadGateway)
		return
	}
	if q.TotalCents == 0 {
		// Free listing: nothing to charge.
		g.forward(w, r, body, nil)
		return
	}

	result, err := g.payment.Charge(r.Context(), server.ChargeParams{
		PaymentAuthorization: r.Header.Get(mpp.HeaderPaymentAuthorization),
		Amount:               fmt.Sprintf("%d.%02d", q.TotalCents/100, q.TotalCents%100),
		Description:          fmt.Sprintf("rentPC: %s x %dh", q.Title, in.Hours),
		ExternalID:           in.ListingID + ":" + fmt.Sprint(in.Hours),
		Body:                 body,
		Meta:                 map[string]string{"listing_id": in.ListingID, "hours": fmt.Sprint(in.Hours)},
	})
	if err != nil {
		if result != nil && result.Challenge != nil {
			server.WritePaymentErrorWithChallenges(w, err, result.Challenges, g.realm)
			return
		}
		server.WritePaymentError(w, err)
		return
	}
	if result.Challenge != nil {
		slog.Info("402 issued", "listing", in.ListingID, "hours", in.Hours, "amount_cents", q.TotalCents)
		server.WriteChallenges(w, result.Challenges, g.realm)
		return
	}

	// Verified on Tempo. Tell the API the money has moved.
	c := claim.Prepaid{Provider: "mpp", Ref: result.Receipt.Reference, AmountCents: q.TotalCents, Currency: q.Currency,
		ListingID: in.ListingID, Hours: in.Hours, Payer: result.Credential.Source}
	sig, err := claim.Sign(g.secret, c)
	if err != nil {
		http.Error(w, "sign failed", http.StatusInternalServerError)
		return
	}
	slog.Info("payment verified", "listing", in.ListingID, "ref", result.Receipt.Reference, "payer", result.Credential.Source)
	g.forward(w, r, body, map[string]string{claim.Header: sig, mpp.HeaderPaymentReceipt: result.Receipt.ToPaymentReceipt()})
}

func (g *gateway) quote(ctx context.Context, listingID string, hours int) (*quote, error) {
	u := *g.api
	u.Path = "/v1/market/" + listingID + "/quote"
	u.RawQuery = "hours=" + fmt.Sprint(hours)
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	resp, err := g.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("quote: %s %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var q quote
	if err := json.NewDecoder(resp.Body).Decode(&q); err != nil {
		return nil, err
	}
	return &q, nil
}

// forward replays the original request to the API with extra headers and
// streams the response back.
func (g *gateway) forward(w http.ResponseWriter, r *http.Request, body []byte, extra map[string]string) {
	u := *g.api
	u.Path, u.RawQuery = r.URL.Path, r.URL.RawQuery
	req, err := http.NewRequestWithContext(r.Context(), r.Method, u.String(), bytes.NewReader(body))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	for k, v := range r.Header {
		if strings.EqualFold(k, mpp.HeaderPaymentAuthorization) {
			continue
		}
		req.Header[k] = v
	}
	for k, v := range extra {
		if k != mpp.HeaderPaymentReceipt {
			req.Header.Set(k, v)
		}
	}
	resp, err := g.client.Do(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()
	for k, v := range resp.Header {
		w.Header()[k] = v
	}
	if rc, ok := extra[mpp.HeaderPaymentReceipt]; ok && resp.StatusCode < 300 {
		w.Header().Set(mpp.HeaderPaymentReceipt, rc)
	}
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)
}
