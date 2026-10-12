// Package rentals implements the rental lifecycle state machine
// (docs/ARCHITECTURE.md §6). Every transition is a guarded UPDATE
// (`WHERE status = <from>`) so concurrent actors cannot corrupt state, and
// every transition appends a rental_events row.
package rentals

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"golang.org/x/crypto/ssh"

	"github.com/cadencz/rentpc/apps/api/internal/db"
	"github.com/cadencz/rentpc/apps/api/internal/payments"
	"github.com/cadencz/rentpc/apps/api/internal/relay"
	"github.com/cadencz/rentpc/packages/protocol"
)

var (
	ErrNotFound     = errors.New("rental not found")
	ErrNotAvailable = errors.New("machine is not available")
	ErrForbidden    = errors.New("forbidden")
	ErrBadState     = errors.New("rental is not in a state that allows this")
	ErrOwnMachine   = errors.New("you cannot rent your own machine")
)

type Config struct {
	ReservationTTL   time.Duration
	ProvisionTimeout time.Duration
	HeartbeatGrace   time.Duration
}

type Service struct {
	DB       *db.DB
	Hub      *relay.Hub
	Payments payments.Provider
	Cfg      Config

	// test hooks; nil means "use Hub".
	online func(hostID string) bool
	send   func(hostID, typ, id string, payload any) error
}

func (s *Service) isOnline(hostID string) bool {
	if s.online != nil {
		return s.online(hostID)
	}
	return s.Hub.Online(hostID)
}

func (s *Service) sendAgent(hostID, typ, id string, payload any) error {
	if s.send != nil {
		return s.send(hostID, typ, id, payload)
	}
	return s.Hub.Send(hostID, typ, id, payload)
}

// Rental is the API representation.
type Rental struct {
	ID                   string                `json:"id"`
	ListingID            string                `json:"listing_id"`
	MachineID            string                `json:"machine_id"`
	HostID               string                `json:"host_id"`
	RenterID             string                `json:"renter_id"`
	HostUserID           string                `json:"host_user_id"`
	Status               protocol.RentalStatus `json:"status"`
	HoursBooked          int                   `json:"hours_booked"`
	PriceCentsPerHour    int                   `json:"price_cents_per_hour"`
	Currency             string                `json:"currency"`
	CPUs                 int                   `json:"cpus"`
	RAMMB                int64                 `json:"ram_mb"`
	DiskGB               int64                 `json:"disk_gb"`
	GPUAccess            protocol.GPUAccess    `json:"gpu_access"`
	ReservationExpiresAt time.Time             `json:"reservation_expires_at"`
	StartsAt             *time.Time            `json:"starts_at"`
	EndsAt               *time.Time            `json:"ends_at"`
	SSHUsername          string                `json:"ssh_username"`
	SSHPubKey            string                `json:"ssh_pubkey"`
	SandboxID            *string               `json:"sandbox_id"`
	FailureReason        *string               `json:"failure_reason"`
	PrepaidProvider      *string               `json:"prepaid_provider"`
	PrepaidRef           *string               `json:"prepaid_ref"`
	HostPayoutCents      *int64                `json:"host_payout_cents"`
	HostPayoutAddress    *string               `json:"host_payout_address"`
	CreatedAt            time.Time             `json:"created_at"`
	UpdatedAt            time.Time             `json:"updated_at"`
	// Derived
	HostOnline         bool   `json:"host_online"`
	EstimatedCostCents int64  `json:"estimated_cost_cents"`
	MachineTitle       string `json:"machine_title,omitempty"`
}

const rentalCols = `r.id, r.listing_id, r.machine_id, r.host_id, r.renter_id, r.host_user_id, r.status,
	r.hours_booked, r.price_cents_per_hour, r.currency, r.cpus, r.ram_mb, r.disk_gb, r.gpu_access,
	r.reservation_expires_at, r.starts_at, r.ends_at, r.ssh_username, r.ssh_pubkey, r.sandbox_id,
	r.failure_reason, r.prepaid_provider, r.prepaid_ref, r.host_payout_cents, r.host_payout_address, r.created_at, r.updated_at, COALESCE(l.title,'')`

const rentalFrom = ` FROM rentals r LEFT JOIN listings l ON l.id = r.listing_id `

func (s *Service) scan(row pgx.Row) (*Rental, error) {
	r := &Rental{}
	err := row.Scan(&r.ID, &r.ListingID, &r.MachineID, &r.HostID, &r.RenterID, &r.HostUserID, &r.Status,
		&r.HoursBooked, &r.PriceCentsPerHour, &r.Currency, &r.CPUs, &r.RAMMB, &r.DiskGB, &r.GPUAccess,
		&r.ReservationExpiresAt, &r.StartsAt, &r.EndsAt, &r.SSHUsername, &r.SSHPubKey, &r.SandboxID,
		&r.FailureReason, &r.PrepaidProvider, &r.PrepaidRef, &r.HostPayoutCents, &r.HostPayoutAddress, &r.CreatedAt, &r.UpdatedAt, &r.MachineTitle)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	r.HostOnline = s.isOnline(r.HostID)
	r.EstimatedCostCents = int64(r.HoursBooked) * int64(r.PriceCentsPerHour)
	return r, nil
}

func (s *Service) Get(ctx context.Context, id string) (*Rental, error) {
	return s.scan(s.DB.QueryRow(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.id=$1`, id))
}

// GetForUser returns the rental if userID is the renter or the host owner.
func (s *Service) GetForUser(ctx context.Context, id, userID string) (*Rental, error) {
	r, err := s.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if r.RenterID != userID && r.HostUserID != userID {
		return nil, ErrForbidden
	}
	return r, nil
}

func (s *Service) ListForRenter(ctx context.Context, userID string) ([]*Rental, error) {
	return s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.renter_id=$1 ORDER BY r.created_at DESC LIMIT 100`, userID)
}

func (s *Service) ListForHostUser(ctx context.Context, userID string) ([]*Rental, error) {
	return s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.host_user_id=$1 ORDER BY r.created_at DESC LIMIT 100`, userID)
}

func (s *Service) list(ctx context.Context, q string, args ...any) ([]*Rental, error) {
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Rental{}
	for rows.Next() {
		r, err := s.scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type execer interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
}

func (s *Service) event(ctx context.Context, q execer, rentalID, typ string, payload any) {
	b, _ := json.Marshal(payload)
	if b == nil {
		b = []byte("{}")
	}
	if _, err := q.Exec(ctx, `INSERT INTO rental_events(rental_id, type, payload) VALUES($1,$2,$3)`, rentalID, typ, b); err != nil {
		slog.Warn("rental event insert failed", "rental", rentalID, "type", typ, "err", err)
	}
}

// transition performs a guarded status change and returns true if it happened.
func (s *Service) transition(ctx context.Context, rentalID string, from []protocol.RentalStatus, to protocol.RentalStatus, extraSet string, args ...any) (bool, error) {
	froms := make([]string, len(from))
	for i, f := range from {
		froms[i] = string(f)
	}
	q := `UPDATE rentals SET status=$2, updated_at=now()` + extraSet + ` WHERE id=$1 AND status = ANY($3)`
	all := append([]any{rentalID, string(to), froms}, args...)
	tag, err := s.DB.Exec(ctx, q, all...)
	if err != nil {
		return false, err
	}
	if tag.RowsAffected() == 0 {
		return false, nil
	}
	s.event(ctx, s.DB, rentalID, "status."+string(to), map[string]any{"from": froms})
	return true, nil
}

// ---- Reserve ----------------------------------------------------------------

type ReserveInput struct {
	RenterID  string
	ListingID string
	Hours     int
	// Prepaid is set when an external protocol (MPP/Tempo, x402) already
	// settled the full amount before the request reached us.
	Prepaid *Prepaid
}

// Prepaid describes an up-front payment verified by the payment gateway.
type Prepaid struct {
	Provider    string // "mpp"
	Ref         string // on-chain reference / receipt id
	AmountCents int64
	Currency    string
	// Direct on-chain payout to the host (split transfer), if any.
	HostPayoutCents   int64
	HostPayoutAddress string
}

// Reserve atomically books the machine, generates the renter's SSH keypair,
// places a payment hold and asks the agent to provision. The returned private
// key is shown to the renter exactly once (it is also kept encrypted for a
// one-time fetch from the rental page, then deleted).
func (s *Service) Reserve(ctx context.Context, in ReserveInput) (*Rental, string, error) {
	if in.Hours < 1 {
		return nil, "", errors.New("hours must be >= 1")
	}
	var (
		machineID, hostID, hostUserID, currency string
		price, cpus, minH, maxH                 int
		ram, disk                               int64
		gpu                                     string
		active                                  bool
	)
	err := s.DB.QueryRow(ctx, `SELECT l.machine_id, h.id, h.user_id, l.price_cents_per_hour, l.currency,
		l.alloc_cpus, l.alloc_ram_mb, l.alloc_disk_gb, l.gpu_access, l.min_hours, l.max_hours, l.is_active
		FROM listings l JOIN machines m ON m.id=l.machine_id JOIN hosts h ON h.id=m.host_id WHERE l.id=$1`, in.ListingID).
		Scan(&machineID, &hostID, &hostUserID, &price, &currency, &cpus, &ram, &disk, &gpu, &minH, &maxH, &active)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, "", ErrNotFound
	}
	if err != nil {
		return nil, "", err
	}
	if hostUserID == in.RenterID {
		return nil, "", ErrOwnMachine
	}
	if !active || !s.isOnline(hostID) {
		return nil, "", ErrNotAvailable
	}
	if in.Hours < minH || in.Hours > maxH {
		return nil, "", fmt.Errorf("hours must be between %d and %d", minH, maxH)
	}

	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, "", err
	}
	sshPub, err := ssh.NewPublicKey(pub)
	if err != nil {
		return nil, "", err
	}
	pubLine := strings.TrimSpace(string(ssh.MarshalAuthorizedKey(sshPub))) + " rentpc"
	privPEM, err := ssh.MarshalPrivateKey(priv, "rentpc rental key")
	if err != nil {
		return nil, "", err
	}
	privText := string(pem.EncodeToMemory(privPEM))

	var prepaidProvider, prepaidRef, payoutAddr *string
	var payoutCents *int64
	if in.Prepaid != nil {
		expected := int64(in.Hours) * int64(price)
		if in.Prepaid.AmountCents < expected {
			return nil, "", fmt.Errorf("prepaid amount %d is less than the rental price %d", in.Prepaid.AmountCents, expected)
		}
		prepaidProvider, prepaidRef = &in.Prepaid.Provider, &in.Prepaid.Ref
		if in.Prepaid.HostPayoutCents > 0 {
			payoutCents, payoutAddr = &in.Prepaid.HostPayoutCents, &in.Prepaid.HostPayoutAddress
		}
	}
	var id string
	err = s.DB.QueryRow(ctx, `INSERT INTO rentals(listing_id, machine_id, host_id, renter_id, host_user_id, status,
		hours_booked, price_cents_per_hour, currency, cpus, ram_mb, disk_gb, gpu_access, reservation_expires_at,
		ssh_pubkey, ssh_privkey, prepaid_provider, prepaid_ref, host_payout_cents, host_payout_address)
		VALUES($1,$2,$3,$4,$5,'reserved',$6,$7,$8,$9,$10,$11,$12,now()+$13::interval,$14,$15,$16,$17,$18,$19) RETURNING id`,
		in.ListingID, machineID, hostID, in.RenterID, hostUserID, in.Hours, price, currency, cpus, ram, disk, gpu,
		s.Cfg.ReservationTTL.String(), pubLine, privText, prepaidProvider, prepaidRef, payoutCents, payoutAddr).Scan(&id)
	if err != nil {
		if strings.Contains(err.Error(), "one_live_rental_per_machine") {
			return nil, "", ErrNotAvailable
		}
		return nil, "", err
	}
	s.event(ctx, s.DB, id, "reserved", map[string]any{"hours": in.Hours})

	if in.Prepaid != nil {
		// Money already moved on-chain: record it in the ledger now.
		amt := payments.Money{AmountCents: in.Prepaid.AmountCents, Currency: in.Prepaid.Currency}
		auth := &payments.Authorization{Ref: in.Prepaid.Ref, Amount: amt, HostPayoutCents: in.Prepaid.HostPayoutCents}
		if err := (&payments.Mock{DB: s.DB}).CaptureAs(ctx, in.Prepaid.Provider, auth, amt, hostUserID, in.RenterID, id, payments.Fee(amt)); err != nil {
			slog.Error("prepaid ledger write failed", "rental", id, "err", err)
		}
		s.event(ctx, s.DB, id, "prepaid", map[string]any{"provider": in.Prepaid.Provider, "ref": in.Prepaid.Ref, "amount_cents": in.Prepaid.AmountCents,
			"host_payout_cents": in.Prepaid.HostPayoutCents, "host_payout_address": in.Prepaid.HostPayoutAddress})
	} else {
		maxCost := payments.Money{AmountCents: int64(in.Hours) * int64(price) * 2, Currency: currency} // allow one extension
		if _, err := s.Payments.Authorize(ctx, in.RenterID, id, maxCost); err != nil {
			_, _ = s.transition(ctx, id, []protocol.RentalStatus{protocol.RentalReserved}, protocol.RentalFailed, `, failure_reason=$4`, "payment authorization failed: "+err.Error())
			return nil, "", fmt.Errorf("payment authorization failed: %w", err)
		}
	}

	spec := protocol.SandboxSpec{
		RentalID: id, CPUs: cpus, RAMMB: ram, DiskGB: disk, GPU: protocol.GPUAccess(gpu),
		SSHPubKey: pubLine, Username: "renter", ExpiresAt: time.Now().Add(time.Duration(in.Hours) * time.Hour),
	}
	if err := s.sendAgent(hostID, protocol.TypeProvision, id, spec); err != nil {
		_, _ = s.transition(ctx, id, []protocol.RentalStatus{protocol.RentalReserved}, protocol.RentalFailed, `, failure_reason=$4`, "host went offline before provisioning")
		return nil, "", ErrNotAvailable
	}
	_, _ = s.transition(ctx, id, []protocol.RentalStatus{protocol.RentalReserved}, protocol.RentalProvisioning, ``)

	r, err := s.Get(ctx, id)
	return r, privText, err
}

// TakePrivateKey returns the stored private key once and deletes it.
func (s *Service) TakePrivateKey(ctx context.Context, rentalID, renterID string) (string, error) {
	var key *string
	err := s.DB.QueryRow(ctx, `UPDATE rentals SET ssh_privkey=NULL WHERE id=$1 AND renter_id=$2 AND ssh_privkey IS NOT NULL RETURNING ssh_privkey`, rentalID, renterID).Scan(&key)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", ErrNotFound
	}
	if err != nil {
		return "", err
	}
	return *key, nil
}

// ---- Agent callbacks ------------------------------------------------------------

func (s *Service) OnProvisionResult(ctx context.Context, hostID string, res protocol.ProvisionResult) {
	r, err := s.Get(ctx, res.RentalID)
	if err != nil || r.HostID != hostID {
		return
	}
	if !res.OK {
		_, _ = s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalReserved, protocol.RentalProvisioning},
			protocol.RentalFailed, `, failure_reason=$4`, "provisioning failed: "+res.Error)
		return
	}
	ok, _ := s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalReserved, protocol.RentalProvisioning},
		protocol.RentalReady, `, sandbox_id=$4, starts_at=now(), ends_at=now() + ($5::int * interval '1 hour')`, res.SandboxID, r.HoursBooked)
	if !ok {
		// Rental was cancelled/expired meanwhile: tear the sandbox down.
		_ = s.sendAgent(hostID, protocol.TypeTerminate, r.ID, protocol.TerminateRequest{RentalID: r.ID, Reason: "stale"})
	}
}

func (s *Service) OnTerminateResult(ctx context.Context, hostID string, res protocol.TerminateResult) {
	r, err := s.Get(ctx, res.RentalID)
	if err != nil || r.HostID != hostID {
		return
	}
	s.complete(ctx, r)
}

func (s *Service) OnSandboxStatus(ctx context.Context, hostID string, st protocol.SandboxStatus) {
	if st.State == protocol.SandboxFailed {
		_, _ = s.transition(ctx, st.RentalID, []protocol.RentalStatus{protocol.RentalReady, protocol.RentalActive, protocol.RentalExtending},
			protocol.RentalFailed, `, failure_reason=$4`, "sandbox failed: "+st.Error)
		return
	}
	_, _ = s.DB.Exec(ctx, `INSERT INTO usage_samples(rental_id, cpu_pct, mem_mb, net_tx, net_rx) VALUES($1,$2,$3,$4,$5) ON CONFLICT DO NOTHING`,
		st.RentalID, st.CPUPct, st.MemMB, st.NetTxB, st.NetRxB)
}

// OnStateReport reconciles after an agent (re)connects: sandboxes the agent
// has that we don't expect are destroyed; rentals we think are live but the
// agent doesn't have are failed.
func (s *Service) OnStateReport(ctx context.Context, hostID string, rep protocol.StateReport) {
	have := map[string]bool{}
	for _, sb := range rep.Sandboxes {
		have[sb.RentalID] = true
	}
	live, err := s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.host_id=$1 AND r.status IN ('ready','active','extending','terminating')`, hostID)
	if err != nil {
		return
	}
	expected := map[string]bool{}
	for _, r := range live {
		expected[r.ID] = true
		if !have[r.ID] {
			_, _ = s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalReady, protocol.RentalActive, protocol.RentalExtending, protocol.RentalTerminating},
				protocol.RentalFailed, `, failure_reason=$4`, "sandbox missing after host reconnect")
			s.settle(ctx, r, true)
		}
	}
	for id := range have {
		if !expected[id] {
			_ = s.sendAgent(hostID, protocol.TypeTerminate, id, protocol.TerminateRequest{RentalID: id, Reason: "orphaned"})
		}
	}
	// Rentals still provisioning when the agent dropped: re-send provision.
	prov, _ := s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.host_id=$1 AND r.status IN ('reserved','provisioning')`, hostID)
	for _, r := range prov {
		if have[r.ID] {
			continue
		}
		_ = s.sendAgent(hostID, protocol.TypeProvision, r.ID, protocol.SandboxSpec{
			RentalID: r.ID, CPUs: r.CPUs, RAMMB: r.RAMMB, DiskGB: r.DiskGB, GPU: r.GPUAccess,
			SSHPubKey: r.SSHPubKey, Username: r.SSHUsername, ExpiresAt: time.Now().Add(time.Duration(r.HoursBooked) * time.Hour),
		})
	}
}

// ---- Renter actions -------------------------------------------------------------

// Activate marks a ready rental active on the renter's first connection.
func (s *Service) Activate(ctx context.Context, rentalID string) {
	_, _ = s.transition(ctx, rentalID, []protocol.RentalStatus{protocol.RentalReady}, protocol.RentalActive, ``)
}

func (s *Service) Extend(ctx context.Context, rentalID, renterID string, hours int) (*Rental, error) {
	if hours < 1 {
		return nil, errors.New("hours must be >= 1")
	}
	r, err := s.Get(ctx, rentalID)
	if err != nil {
		return nil, err
	}
	if r.RenterID != renterID {
		return nil, ErrForbidden
	}
	tag, err := s.DB.Exec(ctx, `UPDATE rentals SET hours_booked = hours_booked + $2, ends_at = ends_at + ($2::int * interval '1 hour'), updated_at=now()
		WHERE id=$1 AND status IN ('ready','active')`, rentalID, hours)
	if err != nil {
		return nil, err
	}
	if tag.RowsAffected() == 0 {
		return nil, ErrBadState
	}
	s.event(ctx, s.DB, rentalID, "extended", map[string]any{"hours": hours})
	r, err = s.Get(ctx, rentalID)
	if err != nil {
		return nil, err
	}
	if r.EndsAt != nil {
		_ = s.sendAgent(r.HostID, protocol.TypeExtend, r.ID, protocol.ExtendRequest{RentalID: r.ID, ExpiresAt: *r.EndsAt})
	}
	return r, nil
}

// Terminate ends a rental early (renter or host) or at expiry (sweeper).
func (s *Service) Terminate(ctx context.Context, rentalID, byUserID, reason string) (*Rental, error) {
	r, err := s.Get(ctx, rentalID)
	if err != nil {
		return nil, err
	}
	if byUserID != "" && r.RenterID != byUserID && r.HostUserID != byUserID {
		return nil, ErrForbidden
	}
	switch r.Status {
	case protocol.RentalReserved, protocol.RentalProvisioning:
		// Nothing is running yet (or it may be mid-creation): cancel and tell the agent just in case.
		if ok, _ := s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalReserved, protocol.RentalProvisioning}, protocol.RentalCancelled, `, failure_reason=$4`, reason); ok {
			_ = s.sendAgent(r.HostID, protocol.TypeTerminate, r.ID, protocol.TerminateRequest{RentalID: r.ID, Reason: reason})
		}
	case protocol.RentalReady, protocol.RentalActive, protocol.RentalExtending:
		ok, err := s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalReady, protocol.RentalActive, protocol.RentalExtending}, protocol.RentalTerminating, `, failure_reason=$4`, reason)
		if err != nil {
			return nil, err
		}
		if ok {
			if err := s.sendAgent(r.HostID, protocol.TypeTerminate, r.ID, protocol.TerminateRequest{RentalID: r.ID, Reason: reason}); err != nil {
				// Host is offline: nothing to wait for; settle now, the agent will reconcile on reconnect.
				r2, _ := s.Get(ctx, r.ID)
				if r2 != nil {
					s.complete(ctx, r2)
				}
			}
		}
	default:
		return nil, ErrBadState
	}
	return s.Get(ctx, rentalID)
}

// complete moves terminating → completed and settles money.
func (s *Service) complete(ctx context.Context, r *Rental) {
	ok, _ := s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalTerminating}, protocol.RentalCompleted, `, ends_at=LEAST(ends_at, now())`)
	if ok {
		s.settle(ctx, r, false)
	}
}

// settle charges the renter for the time actually used (rounded up to the
// hour; prorated to the minute when the failure was not the renter's fault).
func (s *Service) settle(ctx context.Context, r *Rental, prorate bool) {
	if r.PrepaidRef != nil {
		s.event(ctx, s.DB, r.ID, "settled", map[string]any{"prepaid": true, "ref": *r.PrepaidRef})
		return // paid up-front on-chain; nothing to capture (refunds for early termination: roadmap)
	}
	if r.StartsAt == nil {
		return
	}
	used := time.Since(*r.StartsAt)
	if r.EndsAt != nil && r.EndsAt.Before(time.Now()) {
		used = r.EndsAt.Sub(*r.StartsAt)
	}
	var cents int64
	if prorate {
		cents = int64(used.Minutes()+0.5) * int64(r.PriceCentsPerHour) / 60
	} else {
		hours := int64(used.Hours())
		if used-time.Duration(hours)*time.Hour > time.Minute {
			hours++
		}
		if hours < 1 {
			hours = 1
		}
		cents = hours * int64(r.PriceCentsPerHour)
	}
	actual := payments.Money{AmountCents: cents, Currency: r.Currency}
	auth := &payments.Authorization{Ref: "mock_auth_" + r.ID}
	if err := s.Payments.Capture(ctx, auth, actual, r.HostUserID, r.RenterID, r.ID, payments.Fee(actual)); err != nil {
		slog.Error("settle failed", "rental", r.ID, "err", err)
		return
	}
	s.event(ctx, s.DB, r.ID, "settled", map[string]any{"amount_cents": cents, "prorated": prorate})
}

// ---- Sweeper ----------------------------------------------------------------------

// RunSweeper handles timeouts until ctx is done.
func (s *Service) RunSweeper(ctx context.Context, every time.Duration) {
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.sweep(ctx)
		}
	}
}

func (s *Service) sweep(ctx context.Context) {
	// 1. Reservations the agent never picked up.
	expired, _ := s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.status='reserved' AND r.reservation_expires_at < now()`)
	for _, r := range expired {
		_, _ = s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalReserved}, protocol.RentalExpired, ``)
	}
	// 2. Provisioning that took too long.
	stuck, _ := s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.status='provisioning' AND r.updated_at < now() - $1::interval`, s.Cfg.ProvisionTimeout.String())
	for _, r := range stuck {
		if ok, _ := s.transition(ctx, r.ID, []protocol.RentalStatus{protocol.RentalProvisioning}, protocol.RentalFailed, `, failure_reason=$4`, "provisioning timed out"); ok {
			_ = s.sendAgent(r.HostID, protocol.TypeTerminate, r.ID, protocol.TerminateRequest{RentalID: r.ID, Reason: "provision timeout"})
		}
	}
	// 3. Sessions past their end.
	over, _ := s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.status IN ('ready','active','extending') AND r.ends_at < now()`)
	for _, r := range over {
		_, _ = s.Terminate(ctx, r.ID, "", "rental period ended")
	}
	// 4. Terminating rentals whose agent never answered.
	hung, _ := s.list(ctx, `SELECT `+rentalCols+rentalFrom+` WHERE r.status='terminating' AND r.updated_at < now() - interval '2 minutes'`)
	for _, r := range hung {
		s.complete(ctx, r)
	}
	// 5. Hosts whose heartbeat lapsed (connection may still look open).
	_, _ = s.DB.Exec(ctx, `UPDATE hosts SET status='offline' WHERE status <> 'offline' AND (last_heartbeat_at IS NULL OR last_heartbeat_at < now() - $1::interval)`, s.Cfg.HeartbeatGrace.String())
}
