// Package payments defines the seam between rental accounting and whatever
// moves money. The rental lifecycle only ever talks to Provider; the mock
// implementation records ledger rows and nothing else. Tempo / MPP /
// escrow-based providers implement the same interface later.
package payments

import (
	"context"
	"fmt"
	"time"

	"github.com/cadencz/rentpc/apps/api/internal/db"
)

type Money struct {
	AmountCents int64  `json:"amount_cents"`
	Currency    string `json:"currency"`
}

// Authorization is a hold placed when a rental is reserved.
type Authorization struct {
	Ref       string
	Amount    Money
	ExpiresAt time.Time
	// HostPayoutCents is the portion already transferred to the host on-chain
	// (prepaid providers with split payments); 0 for holds.
	HostPayoutCents int64
}

type Provider interface {
	Name() string
	// Authorize places a hold for the maximum the rental could cost.
	Authorize(ctx context.Context, renterID, rentalID string, max Money) (*Authorization, error)
	// Capture settles the actual usage against the hold and credits the host
	// with the gross amount; fee is recorded as a separate negative entry.
	Capture(ctx context.Context, auth *Authorization, actual Money, hostUserID, renterID, rentalID string, fee Money) error
	// Release cancels a hold without charging (failed/expired rentals).
	Release(ctx context.Context, auth *Authorization, rentalID string) error
}

// PlatformFeeBps is the marketplace fee in basis points (10% = 1000).
const PlatformFeeBps = 1000

func Fee(m Money) Money {
	return Money{AmountCents: m.AmountCents * PlatformFeeBps / 10000, Currency: m.Currency}
}

// Mock writes ledger entries and never fails. Good enough for the hackathon
// demo and for tests; swap via config PAYMENT_PROVIDER later.
type Mock struct{ DB *db.DB }

func (m *Mock) Name() string { return "mock" }

func (m *Mock) Authorize(ctx context.Context, renterID, rentalID string, max Money) (*Authorization, error) {
	return &Authorization{
		Ref:       "mock_auth_" + rentalID,
		Amount:    max,
		ExpiresAt: time.Now().Add(48 * time.Hour),
	}, nil
}

func (m *Mock) Capture(ctx context.Context, auth *Authorization, actual Money, hostUserID, renterID, rentalID string, fee Money) error {
	return m.CaptureAs(ctx, m.Name(), auth, actual, hostUserID, renterID, rentalID, fee)
}

// CaptureAs writes the ledger rows under an explicit provider name; used for
// payments verified elsewhere (the MPP gateway) that only need recording.
func (m *Mock) CaptureAs(ctx context.Context, provider string, auth *Authorization, actual Money, hostUserID, renterID, rentalID string, fee Money) error {
	tx, err := m.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	ins := `INSERT INTO ledger_entries(rental_id, user_id, kind, amount_cents, currency, provider, provider_ref) VALUES($1,$2,$3,$4,$5,$6,$7)`
	if _, err := tx.Exec(ctx, ins, rentalID, renterID, "charge", -actual.AmountCents, actual.Currency, provider, auth.Ref); err != nil {
		return err
	}
	// Gross earning + separate negative fee row: the host's net is the sum.
	if _, err := tx.Exec(ctx, ins, rentalID, hostUserID, "earning", actual.AmountCents, actual.Currency, provider, auth.Ref); err != nil {
		return err
	}
	if fee.AmountCents > 0 {
		if _, err := tx.Exec(ctx, ins, rentalID, hostUserID, "fee", -fee.AmountCents, fee.Currency, provider, auth.Ref); err != nil {
			return err
		}
	}
	// Paid straight to the host's wallet on-chain: nothing is owed by the platform.
	if auth.HostPayoutCents > 0 {
		if _, err := tx.Exec(ctx, ins, rentalID, hostUserID, "payout", -auth.HostPayoutCents, actual.Currency, provider, auth.Ref); err != nil {
			return err
		}
	}
	return tx.Commit(ctx)
}

func (m *Mock) Release(ctx context.Context, auth *Authorization, rentalID string) error {
	return nil
}

func (m Money) String() string {
	return fmt.Sprintf("%d.%02d %s", m.AmountCents/100, m.AmountCents%100, m.Currency)
}
