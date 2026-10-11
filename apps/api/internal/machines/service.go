// Package machines covers hosts (the agent identity), machines (detected
// hardware) and listings (what is offered for rent), plus marketplace search.
package machines

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/cadencz/rentpc/apps/api/internal/auth"
	"github.com/cadencz/rentpc/apps/api/internal/db"
	"github.com/cadencz/rentpc/packages/protocol"
)

var (
	ErrNotFound  = errors.New("not found")
	ErrForbidden = errors.New("forbidden")
)

type Service struct {
	DB *db.DB
	// Online tells whether the agent for a host is connected right now.
	Online func(hostID string) bool
}

type Host struct {
	ID              string     `json:"id"`
	UserID          string     `json:"user_id"`
	Name            string     `json:"name"`
	Registered      bool       `json:"registered"`
	AgentVersion    *string    `json:"agent_version"`
	Driver          *string    `json:"driver"`
	Status          string     `json:"status"`
	Online          bool       `json:"online"`
	LastHeartbeatAt *time.Time `json:"last_heartbeat_at"`
	CreatedAt       time.Time  `json:"created_at"`
	Machine         *Machine   `json:"machine,omitempty"`
	Listing         *Listing   `json:"listing,omitempty"`
}

type Machine struct {
	ID         string                  `json:"id"`
	HostID     string                  `json:"host_id"`
	Hostname   string                  `json:"hostname"`
	OSFamily   string                  `json:"os_family"`
	OSVersion  string                  `json:"os_version"`
	Arch       string                  `json:"arch"`
	CPUModel   string                  `json:"cpu_model"`
	CPUCores   int                     `json:"cpu_cores"`
	CPUThreads int                     `json:"cpu_threads"`
	RAMMB      int64                   `json:"ram_mb"`
	GPUVendor  string                  `json:"gpu_vendor"`
	GPUModel   string                  `json:"gpu_model"`
	GPUVRAMMB  int64                   `json:"gpu_vram_mb"`
	DiskFreeGB int64                   `json:"disk_free_gb"`
	Virt       protocol.VirtCapability `json:"virt"`
	Country    string                  `json:"country"`
	Region     string                  `json:"region"`
	DetectedAt time.Time               `json:"detected_at"`
}

type Listing struct {
	ID                string             `json:"id"`
	MachineID         string             `json:"machine_id"`
	Title             string             `json:"title"`
	Description       string             `json:"description"`
	PriceCentsPerHour int                `json:"price_cents_per_hour"`
	Currency          string             `json:"currency"`
	AllocCPUs         int                `json:"alloc_cpus"`
	AllocRAMMB        int64              `json:"alloc_ram_mb"`
	AllocDiskGB       int64              `json:"alloc_disk_gb"`
	GPUAccess         protocol.GPUAccess `json:"gpu_access"`
	MinHours          int                `json:"min_hours"`
	MaxHours          int                `json:"max_hours"`
	IsActive          bool               `json:"is_active"`
	UpdatedAt         time.Time          `json:"updated_at"`
}

// MarketItem is a searchable, rentable machine as shown to renters.
type MarketItem struct {
	Listing   Listing  `json:"listing"`
	Machine   Machine  `json:"machine"`
	HostID    string   `json:"host_id"`
	HostName  string   `json:"host_name"`
	OwnerName string   `json:"owner_name"`
	Online    bool     `json:"online"`
	Available bool     `json:"available"` // online, active and not currently rented
	Rating    *float64 `json:"rating"`
	Rentals   int      `json:"completed_rentals"`
}

// ---- Hosts ----------------------------------------------------------------------

// CreateHost creates a host record and returns a one-time registration token
// (valid 1 hour) the owner pastes into `rentpc-host register`.
func (s *Service) CreateHost(ctx context.Context, userID, name string) (*Host, string, error) {
	if name = strings.TrimSpace(name); name == "" {
		name = "My PC"
	}
	tok, hash, err := auth.NewToken()
	if err != nil {
		return nil, "", err
	}
	var id string
	if err := s.DB.QueryRow(ctx, `INSERT INTO hosts(user_id, name, register_token_hash, register_expires_at)
		VALUES($1,$2,$3,now()+interval '1 hour') RETURNING id`, userID, name, hash).Scan(&id); err != nil {
		return nil, "", err
	}
	h, err := s.GetHost(ctx, id)
	return h, tok, err
}

// RotateRegisterToken issues a fresh one-time token for an existing host.
func (s *Service) RotateRegisterToken(ctx context.Context, hostID, userID string) (string, error) {
	tok, hash, err := auth.NewToken()
	if err != nil {
		return "", err
	}
	tag, err := s.DB.Exec(ctx, `UPDATE hosts SET register_token_hash=$3, register_expires_at=now()+interval '1 hour' WHERE id=$1 AND user_id=$2`, hostID, userID, hash)
	if err != nil {
		return "", err
	}
	if tag.RowsAffected() == 0 {
		return "", ErrNotFound
	}
	return tok, nil
}

// RedeemRegisterToken is called by the agent: it exchanges the one-time token
// for a long-lived agent token and returns (hostID, agentToken).
func (s *Service) RedeemRegisterToken(ctx context.Context, regToken string) (string, string, error) {
	agentTok, agentHash, err := auth.NewToken()
	if err != nil {
		return "", "", err
	}
	var id string
	err = s.DB.QueryRow(ctx, `UPDATE hosts SET agent_token_hash=$2, register_token_hash=NULL, register_expires_at=NULL
		WHERE register_token_hash=$1 AND register_expires_at > now() RETURNING id`, auth.HashToken(regToken), agentHash).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", "", errors.New("registration token is invalid or expired")
	}
	if err != nil {
		return "", "", err
	}
	return id, agentTok, nil
}

// HostByAgentToken authenticates an agent connection.
func (s *Service) HostByAgentToken(ctx context.Context, tok string) (string, error) {
	var id string
	err := s.DB.QueryRow(ctx, `SELECT id FROM hosts WHERE agent_token_hash=$1`, auth.HashToken(tok)).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", errors.New("invalid agent token")
	}
	return id, err
}

func (s *Service) GetHost(ctx context.Context, id string) (*Host, error) {
	h := &Host{}
	var agentHash *string
	err := s.DB.QueryRow(ctx, `SELECT id, user_id, name, agent_token_hash, agent_version, driver, status, last_heartbeat_at, created_at FROM hosts WHERE id=$1`, id).
		Scan(&h.ID, &h.UserID, &h.Name, &agentHash, &h.AgentVersion, &h.Driver, &h.Status, &h.LastHeartbeatAt, &h.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	h.Registered = agentHash != nil
	if s.Online != nil {
		h.Online = s.Online(h.ID)
	}
	h.Machine, _ = s.MachineByHost(ctx, h.ID)
	if h.Machine != nil {
		h.Listing, _ = s.ListingByMachine(ctx, h.Machine.ID)
	}
	return h, nil
}

func (s *Service) HostsForUser(ctx context.Context, userID string) ([]*Host, error) {
	rows, err := s.DB.Query(ctx, `SELECT id FROM hosts WHERE user_id=$1 ORDER BY created_at`, userID)
	if err != nil {
		return nil, err
	}
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	rows.Close()
	out := []*Host{}
	for _, id := range ids {
		h, err := s.GetHost(ctx, id)
		if err != nil {
			return nil, err
		}
		out = append(out, h)
	}
	return out, nil
}

func (s *Service) DeleteHost(ctx context.Context, hostID, userID string) error {
	tag, err := s.DB.Exec(ctx, `DELETE FROM hosts WHERE id=$1 AND user_id=$2 AND NOT EXISTS (
		SELECT 1 FROM rentals WHERE host_id=$1 AND status IN ('reserved','provisioning','ready','active','extending','terminating'))`, hostID, userID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return errors.New("host not found or has a live rental")
	}
	return nil
}

// ---- Agent lifecycle --------------------------------------------------------------

// OnHello records the agent's hardware report and marks the host online.
func (s *Service) OnHello(ctx context.Context, hostID string, h protocol.Hello) error {
	hw := h.Hardware
	gpuVendor, gpuModel, vram := "", "", int64(0)
	if len(hw.GPUs) > 0 {
		gpuVendor, gpuModel, vram = hw.GPUs[0].Vendor, hw.GPUs[0].Model, hw.GPUs[0].VRAMMB
	}
	virt, _ := json.Marshal(hw.Virt)
	tx, err := s.DB.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `INSERT INTO machines(host_id, hostname, os_family, os_version, arch, cpu_model, cpu_cores, cpu_threads,
		ram_mb, gpu_vendor, gpu_model, gpu_vram_mb, disk_free_gb, virt, detected_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,now())
		ON CONFLICT (host_id) DO UPDATE SET hostname=EXCLUDED.hostname, os_family=EXCLUDED.os_family, os_version=EXCLUDED.os_version,
		arch=EXCLUDED.arch, cpu_model=EXCLUDED.cpu_model, cpu_cores=EXCLUDED.cpu_cores, cpu_threads=EXCLUDED.cpu_threads,
		ram_mb=EXCLUDED.ram_mb, gpu_vendor=EXCLUDED.gpu_vendor, gpu_model=EXCLUDED.gpu_model, gpu_vram_mb=EXCLUDED.gpu_vram_mb,
		disk_free_gb=EXCLUDED.disk_free_gb, virt=EXCLUDED.virt, detected_at=now()`,
		hostID, hw.Hostname, hw.OSFamily, hw.OSVersion, hw.Arch, hw.CPUModel, hw.CPUCores, hw.CPUThreads,
		hw.RAMMB, gpuVendor, gpuModel, vram, hw.DiskFreeGB, virt); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `UPDATE hosts SET status='online', agent_version=$2, driver=$3, last_heartbeat_at=now() WHERE id=$1`,
		hostID, h.AgentVersion, h.Driver); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

func (s *Service) OnHeartbeat(ctx context.Context, hostID string, hb protocol.Heartbeat) {
	status := "online"
	if len(hb.Sandboxes) > 0 {
		status = "busy"
	}
	_, _ = s.DB.Exec(ctx, `UPDATE hosts SET status=$2, last_heartbeat_at=now() WHERE id=$1`, hostID, status)
}

func (s *Service) OnDisconnect(ctx context.Context, hostID string) {
	_, _ = s.DB.Exec(ctx, `UPDATE hosts SET status='offline' WHERE id=$1`, hostID)
}

// ---- Machines ----------------------------------------------------------------------

const machineCols = `m.id, m.host_id, m.hostname, m.os_family, m.os_version, m.arch, m.cpu_model, m.cpu_cores, m.cpu_threads,
	m.ram_mb, m.gpu_vendor, m.gpu_model, m.gpu_vram_mb, m.disk_free_gb, m.virt, m.country, m.region, m.detected_at`

func scanMachine(row pgx.Row) (*Machine, error) {
	m := &Machine{}
	var virt []byte
	err := row.Scan(&m.ID, &m.HostID, &m.Hostname, &m.OSFamily, &m.OSVersion, &m.Arch, &m.CPUModel, &m.CPUCores, &m.CPUThreads,
		&m.RAMMB, &m.GPUVendor, &m.GPUModel, &m.GPUVRAMMB, &m.DiskFreeGB, &virt, &m.Country, &m.Region, &m.DetectedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal(virt, &m.Virt)
	return m, nil
}

func (s *Service) MachineByHost(ctx context.Context, hostID string) (*Machine, error) {
	return scanMachine(s.DB.QueryRow(ctx, `SELECT `+machineCols+` FROM machines m WHERE m.host_id=$1`, hostID))
}

func (s *Service) SetMachineLocation(ctx context.Context, hostID, userID, country, region string) error {
	tag, err := s.DB.Exec(ctx, `UPDATE machines m SET country=$3, region=$4 FROM hosts h WHERE h.id=m.host_id AND h.id=$1 AND h.user_id=$2`, hostID, userID, country, region)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---- Listings ------------------------------------------------------------------------

const listingColsBare = `id, machine_id, title, description, price_cents_per_hour, currency, alloc_cpus, alloc_ram_mb,
	alloc_disk_gb, gpu_access, min_hours, max_hours, is_active, updated_at`

const listingCols = `l.id, l.machine_id, l.title, l.description, l.price_cents_per_hour, l.currency, l.alloc_cpus, l.alloc_ram_mb,
	l.alloc_disk_gb, l.gpu_access, l.min_hours, l.max_hours, l.is_active, l.updated_at`

func scanListing(row pgx.Row) (*Listing, error) {
	l := &Listing{}
	err := row.Scan(&l.ID, &l.MachineID, &l.Title, &l.Description, &l.PriceCentsPerHour, &l.Currency, &l.AllocCPUs, &l.AllocRAMMB,
		&l.AllocDiskGB, &l.GPUAccess, &l.MinHours, &l.MaxHours, &l.IsActive, &l.UpdatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	return l, err
}

func (s *Service) ListingByMachine(ctx context.Context, machineID string) (*Listing, error) {
	return scanListing(s.DB.QueryRow(ctx, `SELECT `+listingCols+` FROM listings l WHERE l.machine_id=$1`, machineID))
}

type ListingInput struct {
	Title             string             `json:"title"`
	Description       string             `json:"description"`
	PriceCentsPerHour int                `json:"price_cents_per_hour"`
	Currency          string             `json:"currency"`
	AllocCPUs         int                `json:"alloc_cpus"`
	AllocRAMMB        int64              `json:"alloc_ram_mb"`
	AllocDiskGB       int64              `json:"alloc_disk_gb"`
	GPUAccess         protocol.GPUAccess `json:"gpu_access"`
	MinHours          int                `json:"min_hours"`
	MaxHours          int                `json:"max_hours"`
	IsActive          bool               `json:"is_active"`
}

// UpsertListing creates or updates the listing for the user's host. It
// clamps the allocation to what the machine actually has (leaving the owner
// headroom) and refuses GPU access unless the host's driver supports it —
// GPU is never enabled by wishful thinking (SECURITY.md).
func (s *Service) UpsertListing(ctx context.Context, hostID, userID string, in ListingInput) (*Listing, error) {
	h, err := s.GetHost(ctx, hostID)
	if err != nil {
		return nil, err
	}
	if h.UserID != userID {
		return nil, ErrForbidden
	}
	if h.Machine == nil {
		return nil, errors.New("the host agent has not reported hardware yet; run `rentpc-host run` first")
	}
	m := h.Machine
	if in.Title = strings.TrimSpace(in.Title); in.Title == "" {
		in.Title = fmt.Sprintf("%s · %d vCPU · %d GB RAM", m.CPUModel, in.AllocCPUs, in.AllocRAMMB/1024)
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if in.MinHours < 1 {
		in.MinHours = 1
	}
	if in.MaxHours < in.MinHours {
		in.MaxHours = 24
	}
	if in.PriceCentsPerHour < 0 {
		return nil, errors.New("price must be >= 0")
	}
	maxCPU := m.CPUThreads - 1
	if maxCPU < 1 {
		maxCPU = 1
	}
	if in.AllocCPUs < 1 || in.AllocCPUs > maxCPU {
		return nil, fmt.Errorf("alloc_cpus must be between 1 and %d (machine has %d threads, one is kept for the owner)", maxCPU, m.CPUThreads)
	}
	maxRAM := m.RAMMB - 4096
	if maxRAM < 1024 {
		maxRAM = m.RAMMB / 2
	}
	if in.AllocRAMMB < 512 || in.AllocRAMMB > maxRAM {
		return nil, fmt.Errorf("alloc_ram_mb must be between 512 and %d", maxRAM)
	}
	if in.AllocDiskGB < 5 || in.AllocDiskGB > m.DiskFreeGB-10 {
		return nil, fmt.Errorf("alloc_disk_gb must be between 5 and %d", m.DiskFreeGB-10)
	}
	switch in.GPUAccess {
	case "", protocol.GPUNone:
		in.GPUAccess = protocol.GPUNone
	default:
		return nil, errors.New("GPU access is not available for this host's driver yet (CPU-only tier)")
	}
	l, err := scanListing(s.DB.QueryRow(ctx, `INSERT INTO listings(machine_id, title, description, price_cents_per_hour, currency,
		alloc_cpus, alloc_ram_mb, alloc_disk_gb, gpu_access, min_hours, max_hours, is_active)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
		ON CONFLICT (machine_id) DO UPDATE SET title=EXCLUDED.title, description=EXCLUDED.description,
		price_cents_per_hour=EXCLUDED.price_cents_per_hour, currency=EXCLUDED.currency, alloc_cpus=EXCLUDED.alloc_cpus,
		alloc_ram_mb=EXCLUDED.alloc_ram_mb, alloc_disk_gb=EXCLUDED.alloc_disk_gb, gpu_access=EXCLUDED.gpu_access,
		min_hours=EXCLUDED.min_hours, max_hours=EXCLUDED.max_hours, is_active=EXCLUDED.is_active, updated_at=now()
		RETURNING `+listingColsBare,
		m.ID, in.Title, in.Description, in.PriceCentsPerHour, in.Currency, in.AllocCPUs, in.AllocRAMMB, in.AllocDiskGB,
		string(in.GPUAccess), in.MinHours, in.MaxHours, in.IsActive))
	return l, err
}

// ---- Search ------------------------------------------------------------------------------

type SearchFilter struct {
	Query      string
	OSFamily   string
	GPUVendor  string
	MinCPUs    int
	MinRAMMB   int64
	MinVRAMMB  int64
	MaxPrice   int // cents/hour
	Country    string
	OnlineOnly bool
	Sort       string // price_asc | price_desc | newest | ram_desc
	Limit      int
	Offset     int
}

func (s *Service) Search(ctx context.Context, f SearchFilter) ([]MarketItem, error) {
	var where []string
	var args []any
	add := func(cond string, v any) {
		args = append(args, v)
		where = append(where, fmt.Sprintf(cond, len(args)))
	}
	where = append(where, "l.is_active")
	if f.Query != "" {
		add("(l.title ILIKE '%%' || $%d || '%%' OR m.cpu_model ILIKE '%%' || $%[1]d || '%%' OR m.gpu_model ILIKE '%%' || $%[1]d || '%%')", f.Query)
	}
	if f.OSFamily != "" {
		add("m.os_family = $%d", f.OSFamily)
	}
	if f.GPUVendor != "" {
		add("m.gpu_vendor ILIKE $%d", f.GPUVendor)
	}
	if f.MinCPUs > 0 {
		add("l.alloc_cpus >= $%d", f.MinCPUs)
	}
	if f.MinRAMMB > 0 {
		add("l.alloc_ram_mb >= $%d", f.MinRAMMB)
	}
	if f.MinVRAMMB > 0 {
		add("m.gpu_vram_mb >= $%d", f.MinVRAMMB)
	}
	if f.MaxPrice > 0 {
		add("l.price_cents_per_hour <= $%d", f.MaxPrice)
	}
	if f.Country != "" {
		add("m.country = $%d", f.Country)
	}
	if f.OnlineOnly {
		where = append(where, "h.status IN ('online','busy')")
	}
	order := "h.status IN ('online','busy') DESC, l.price_cents_per_hour ASC"
	switch f.Sort {
	case "price_desc":
		order = "l.price_cents_per_hour DESC"
	case "newest":
		order = "l.updated_at DESC"
	case "ram_desc":
		order = "l.alloc_ram_mb DESC"
	}
	if f.Limit <= 0 || f.Limit > 100 {
		f.Limit = 50
	}
	q := `SELECT ` + listingCols + `, ` + machineCols + `, h.id, h.name, h.status, u.display_name,
		EXISTS(SELECT 1 FROM rentals r WHERE r.machine_id=m.id AND r.status IN ('reserved','provisioning','ready','active','extending','terminating')) AS rented,
		(SELECT AVG(rating)::float8 FROM reviews rv WHERE rv.target_user_id=h.user_id) AS rating,
		(SELECT COUNT(*) FROM rentals r2 WHERE r2.host_id=h.id AND r2.status='completed')::int AS completed
		FROM listings l JOIN machines m ON m.id=l.machine_id JOIN hosts h ON h.id=m.host_id JOIN users u ON u.id=h.user_id
		WHERE ` + strings.Join(where, " AND ") + ` ORDER BY ` + order + fmt.Sprintf(" LIMIT %d OFFSET %d", f.Limit, f.Offset)
	rows, err := s.DB.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MarketItem{}
	for rows.Next() {
		var it MarketItem
		var virt []byte
		var hostStatus string
		var rented bool
		l, m := &it.Listing, &it.Machine
		if err := rows.Scan(&l.ID, &l.MachineID, &l.Title, &l.Description, &l.PriceCentsPerHour, &l.Currency, &l.AllocCPUs, &l.AllocRAMMB,
			&l.AllocDiskGB, &l.GPUAccess, &l.MinHours, &l.MaxHours, &l.IsActive, &l.UpdatedAt,
			&m.ID, &m.HostID, &m.Hostname, &m.OSFamily, &m.OSVersion, &m.Arch, &m.CPUModel, &m.CPUCores, &m.CPUThreads,
			&m.RAMMB, &m.GPUVendor, &m.GPUModel, &m.GPUVRAMMB, &m.DiskFreeGB, &virt, &m.Country, &m.Region, &m.DetectedAt,
			&it.HostID, &it.HostName, &hostStatus, &it.OwnerName, &rented, &it.Rating, &it.Rentals); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(virt, &m.Virt)
		it.Online = hostStatus == "online" || hostStatus == "busy"
		if s.Online != nil {
			it.Online = s.Online(it.HostID)
		}
		it.Available = it.Online && !rented
		out = append(out, it)
	}
	return out, rows.Err()
}

// MarketItemByListing returns the detail view for a single listing.
func (s *Service) MarketItemByListing(ctx context.Context, listingID string) (*MarketItem, error) {
	items, err := s.searchByID(ctx, listingID)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	return &items[0], nil
}

func (s *Service) searchByID(ctx context.Context, listingID string) ([]MarketItem, error) {
	q := `SELECT ` + listingCols + `, ` + machineCols + `, h.id, h.name, h.status, u.display_name,
		EXISTS(SELECT 1 FROM rentals r WHERE r.machine_id=m.id AND r.status IN ('reserved','provisioning','ready','active','extending','terminating')) AS rented,
		(SELECT AVG(rating)::float8 FROM reviews rv WHERE rv.target_user_id=h.user_id) AS rating,
		(SELECT COUNT(*) FROM rentals r2 WHERE r2.host_id=h.id AND r2.status='completed')::int AS completed
		FROM listings l JOIN machines m ON m.id=l.machine_id JOIN hosts h ON h.id=m.host_id JOIN users u ON u.id=h.user_id
		WHERE l.id=$1`
	rows, err := s.DB.Query(ctx, q, listingID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MarketItem{}
	for rows.Next() {
		var it MarketItem
		var virt []byte
		var hostStatus string
		var rented bool
		l, m := &it.Listing, &it.Machine
		if err := rows.Scan(&l.ID, &l.MachineID, &l.Title, &l.Description, &l.PriceCentsPerHour, &l.Currency, &l.AllocCPUs, &l.AllocRAMMB,
			&l.AllocDiskGB, &l.GPUAccess, &l.MinHours, &l.MaxHours, &l.IsActive, &l.UpdatedAt,
			&m.ID, &m.HostID, &m.Hostname, &m.OSFamily, &m.OSVersion, &m.Arch, &m.CPUModel, &m.CPUCores, &m.CPUThreads,
			&m.RAMMB, &m.GPUVendor, &m.GPUModel, &m.GPUVRAMMB, &m.DiskFreeGB, &virt, &m.Country, &m.Region, &m.DetectedAt,
			&it.HostID, &it.HostName, &hostStatus, &it.OwnerName, &rented, &it.Rating, &it.Rentals); err != nil {
			return nil, err
		}
		_ = json.Unmarshal(virt, &m.Virt)
		it.Online = hostStatus == "online" || hostStatus == "busy"
		if s.Online != nil {
			it.Online = s.Online(it.HostID)
		}
		it.Available = it.Online && !rented
		out = append(out, it)
	}
	return out, rows.Err()
}

// Earnings summarises the ledger for a host owner.
type Earnings struct {
	TotalCents       int64   `json:"total_cents"`
	Last30DaysCents  int64   `json:"last_30_days_cents"`
	CompletedRentals int     `json:"completed_rentals"`
	HoursRented      float64 `json:"hours_rented"`
}

func (s *Service) EarningsForUser(ctx context.Context, userID string) (*Earnings, error) {
	e := &Earnings{}
	err := s.DB.QueryRow(ctx, `SELECT
		COALESCE(SUM(amount_cents) FILTER (WHERE kind IN ('earning','fee')),0),
		COALESCE(SUM(amount_cents) FILTER (WHERE kind IN ('earning','fee') AND at > now()-interval '30 days'),0),
		(SELECT COUNT(*) FROM rentals WHERE host_user_id=$1 AND status='completed')::int,
		COALESCE((SELECT SUM(EXTRACT(EPOCH FROM (LEAST(ends_at, now()) - starts_at))/3600) FROM rentals WHERE host_user_id=$1 AND starts_at IS NOT NULL AND status='completed'),0)
		FROM ledger_entries WHERE user_id=$1`, userID).Scan(&e.TotalCents, &e.Last30DaysCents, &e.CompletedRentals, &e.HoursRented)
	return e, err
}
