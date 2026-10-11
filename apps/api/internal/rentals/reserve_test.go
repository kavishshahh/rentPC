package rentals

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cadencz/rentpc/apps/api/internal/auth"
	"github.com/cadencz/rentpc/apps/api/internal/db"
	"github.com/cadencz/rentpc/apps/api/internal/machines"
	"github.com/cadencz/rentpc/apps/api/internal/payments"
	"github.com/cadencz/rentpc/apps/api/internal/relay"
	"github.com/cadencz/rentpc/packages/protocol"
)

// TestConcurrentReserve books one machine from 50 goroutines; exactly one
// must win. Requires TEST_DATABASE_URL (a scratch Postgres); skipped otherwise.
func TestConcurrentReserve(t *testing.T) {
	url := os.Getenv("TEST_DATABASE_URL")
	if url == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	d, err := db.Open(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	defer d.Close()
	if err := d.Migrate(ctx); err != nil {
		t.Fatal(err)
	}
	a := &auth.Service{DB: d, SessionTTL: time.Hour}
	suffix := time.Now().Format("150405.000000")
	owner, err := a.Register(ctx, "owner-"+suffix+"@test", "password123", "owner")
	if err != nil {
		t.Fatal(err)
	}
	hub := relay.New(relay.Events{}, 1)
	m := &machines.Service{DB: d, Online: func(string) bool { return true }} // pretend the agent is connected
	host, _, err := m.CreateHost(ctx, owner.ID, "box")
	if err != nil {
		t.Fatal(err)
	}
	if err := m.OnHello(ctx, host.ID, protocol.Hello{Hardware: protocol.Hardware{Hostname: "h", OSFamily: "linux", CPUThreads: 8, CPUCores: 4, RAMMB: 16384, DiskFreeGB: 200}}); err != nil {
		t.Fatal(err)
	}
	listing, err := m.UpsertListing(ctx, host.ID, owner.ID, machines.ListingInput{PriceCentsPerHour: 100, AllocCPUs: 2, AllocRAMMB: 4096, AllocDiskGB: 20, IsActive: true})
	if err != nil {
		t.Fatal(err)
	}

	// Rentals service whose hub has no real agent: Send fails → rental is
	// marked failed after reservation. To test the *reservation* race we
	// use a hub stub that reports online and swallows sends.
	svc := &Service{DB: d, Hub: hub, Payments: &payments.Mock{DB: d}, Cfg: Config{ReservationTTL: time.Minute, ProvisionTimeout: time.Minute, HeartbeatGrace: time.Minute}}
	svc.online = func(string) bool { return true }
	svc.send = func(string, string, string, any) error { return nil }

	const n = 50
	var wg sync.WaitGroup
	var mu sync.Mutex
	wins, conflicts := 0, 0
	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			renter, err := a.Register(ctx, "renter-"+suffix+"-"+string(rune('a'+i%26))+string(rune('a'+i/26))+"@test", "password123", "r")
			if err != nil {
				t.Error(err)
				return
			}
			_, _, err = svc.Reserve(ctx, ReserveInput{RenterID: renter.ID, ListingID: listing.ID, Hours: 1})
			mu.Lock()
			defer mu.Unlock()
			switch {
			case err == nil:
				wins++
			case errors.Is(err, ErrNotAvailable):
				conflicts++
			default:
				t.Errorf("unexpected error: %v", err)
			}
		}(i)
	}
	wg.Wait()
	if wins != 1 || conflicts != n-1 {
		t.Fatalf("expected exactly 1 winner and %d conflicts, got %d winners / %d conflicts", n-1, wins, conflicts)
	}
}
