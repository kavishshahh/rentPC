// rentpc-pay: rent a machine as an agent would — POST /v1/rentals, receive
// HTTP 402 (MPP), pay in stablecoins on Tempo, retry, get the rental.
//
//	export RENTPC_API_KEY=rpk_...   MPP_PRIVATE_KEY=0x...
//	rentpc-pay --server http://44.222.123.73 --listing <id> --hours 1 --max-spend 2.00
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"

	"github.com/tempoxyz/mpp-go/pkg/client"
	"github.com/tempoxyz/mpp-go/pkg/mpp"
	"github.com/tempoxyz/mpp-go/pkg/tempo"
	charge "github.com/tempoxyz/mpp-go/pkg/tempo/client"
)

func main() {
	server := flag.String("server", envOr("RENTPC_SERVER", "http://localhost"), "rentPC URL")
	listing := flag.String("listing", "", "listing id to rent")
	hours := flag.Int("hours", 1, "hours to book")
	maxSpend := flag.String("max-spend", "1.00", "refuse to pay more than this (in token units, e.g. OUSD)")
	rpc := flag.String("rpc", envOr("TEMPO_RPC_URL", "https://rpc.moderato.tempo.xyz"), "Tempo RPC")
	flag.Parse()
	apiKey := os.Getenv("RENTPC_API_KEY")
	pk := os.Getenv("MPP_PRIVATE_KEY")
	if *listing == "" || apiKey == "" || pk == "" {
		fmt.Fprintln(os.Stderr, "need --listing, RENTPC_API_KEY and MPP_PRIVATE_KEY")
		os.Exit(2)
	}

	// Budget policy: never sign a payment above --max-spend (6-decimal stablecoin units).
	limit, ok := new(big.Rat).SetString(*maxSpend)
	if !ok {
		fmt.Fprintln(os.Stderr, "bad --max-spend")
		os.Exit(2)
	}
	limitBase := new(big.Int).Div(new(big.Int).Mul(limit.Num(), big.NewInt(1_000_000)), limit.Denom())

	method, err := charge.New(charge.Config{
		PrivateKey: pk,
		RPCURL:     *rpc,
		PaymentPolicy: func(_ context.Context, req tempo.ChargeRequest) error {
			amt, ok := new(big.Int).SetString(req.Amount, 10)
			if !ok || amt.Sign() < 0 || amt.Cmp(limitBase) > 0 {
				return fmt.Errorf("challenge asks for %s base units, above --max-spend %s", req.Amount, *maxSpend)
			}
			fmt.Fprintf(os.Stderr, "paying %s (base units) of %s to %s on Tempo…\n", req.Amount, req.Currency, req.Recipient)
			return nil
		},
	})
	if err != nil {
		fatal(err)
	}
	c := client.New([]client.Method{method})

	body, _ := json.Marshal(map[string]any{"listing_id": *listing, "hours": *hours})
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, strings.TrimRight(*server, "/")+"/v1/rentals", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
	resp, err := c.Do(req) // handles 402 → pay → retry
	if err != nil {
		fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	if rc := resp.Header.Get(mpp.HeaderPaymentReceipt); rc != "" {
		if r, err := mpp.ParseReceipt(rc); err == nil {
			fmt.Fprintf(os.Stderr, "payment receipt: %s (%s)\n", r.Reference, r.Status)
		}
	}
	fmt.Println(string(out))
	if resp.StatusCode >= 300 {
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func fatal(err error) { fmt.Fprintln(os.Stderr, "error:", err); os.Exit(1) }
