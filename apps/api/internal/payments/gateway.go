package payments

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// PrepaidClaim is what the payment gateway (services/paygate) attaches to a
// request after it has verified an on-chain payment. The API trusts it only
// when the HMAC (shared PAYGATE_SECRET) checks out and it is fresh.
//
// Header: X-RentPC-Prepaid: <base64url(json)>.<base64url(hmac-sha256)>
type PrepaidClaim struct {
	Provider    string `json:"provider"` // "mpp"
	Ref         string `json:"ref"`      // receipt reference (tx hash)
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
	ListingID   string `json:"listing_id"`
	Hours       int    `json:"hours"`
	Payer       string `json:"payer,omitempty"`
	// HostPayoutCents/Address: the part of the amount that went straight to
	// the host's wallet via an MPP split transfer (0 / "" when the host has no
	// payout wallet and the treasury holds the funds).
	HostPayoutCents   int64  `json:"host_payout_cents,omitempty"`
	HostPayoutAddress string `json:"host_payout_address,omitempty"`
	IssuedAt          int64  `json:"iat"`
}

const PrepaidHeader = "X-RentPC-Prepaid"

func SignPrepaid(secret string, c PrepaidClaim) (string, error) {
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

func VerifyPrepaid(secret, header string) (*PrepaidClaim, error) {
	if secret == "" {
		return nil, errors.New("payment gateway is not configured")
	}
	parts := strings.Split(header, ".")
	if len(parts) != 2 {
		return nil, errors.New("malformed prepaid header")
	}
	body, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return nil, err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, err
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	if !hmac.Equal(sig, mac.Sum(nil)) {
		return nil, errors.New("prepaid signature invalid")
	}
	var c PrepaidClaim
	if err := json.Unmarshal(body, &c); err != nil {
		return nil, err
	}
	if time.Since(time.Unix(c.IssuedAt, 0)) > 2*time.Minute {
		return nil, errors.New("prepaid claim expired")
	}
	return &c, nil
}
