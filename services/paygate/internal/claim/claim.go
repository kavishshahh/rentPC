// Package claim mirrors apps/api/internal/payments/gateway.go: a signed
// statement that a rental was paid up-front, trusted by the API via HMAC.
package claim

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"time"
)

const Header = "X-RentPC-Prepaid"

type Prepaid struct {
	Provider    string `json:"provider"`
	Ref         string `json:"ref"`
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	ListingID   string `json:"listing_id"`
	Hours       int    `json:"hours"`
	Payer       string `json:"payer,omitempty"`
	// Portion routed straight to the host's wallet via an MPP split transfer.
	HostPayoutCents   int64  `json:"host_payout_cents,omitempty"`
	HostPayoutAddress string `json:"host_payout_address,omitempty"`
	IssuedAt          int64  `json:"iat"`
}

func Sign(secret string, c Prepaid) (string, error) {
	if c.IssuedAt == 0 {
		c.IssuedAt = time.Now().Unix()
	}
	body, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return base64.RawURLEncoding.EncodeToString(body) + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}
