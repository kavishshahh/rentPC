// rentPC backend: REST API + agent relay + lifecycle sweeper.
package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/cadencz/rentpc/apps/api/internal/auth"
	"github.com/cadencz/rentpc/apps/api/internal/config"
	"github.com/cadencz/rentpc/apps/api/internal/db"
	apihttp "github.com/cadencz/rentpc/apps/api/internal/http"
	"github.com/cadencz/rentpc/apps/api/internal/machines"
	"github.com/cadencz/rentpc/apps/api/internal/payments"
	"github.com/cadencz/rentpc/apps/api/internal/relay"
	"github.com/cadencz/rentpc/apps/api/internal/rentals"
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stdout, nil)))
	cfg, err := config.Load()
	if err != nil {
		slog.Error("config", "err", err)
		os.Exit(1)
	}
	if cfg.DevMode {
		slog.SetDefault(slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelDebug})))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	database, err := db.Open(ctx, cfg.DatabaseURL)
	if err != nil {
		slog.Error("database", "err", err)
		os.Exit(1)
	}
	defer database.Close()
	if err := database.Migrate(ctx); err != nil {
		slog.Error("migrate", "err", err)
		os.Exit(1)
	}

	authSvc := &auth.Service{DB: database, SessionTTL: cfg.SessionTTL}
	var hub *relay.Hub
	machSvc := &machines.Service{DB: database, Online: func(id string) bool { return hub != nil && hub.Online(id) }}
	rentSvc := &rentals.Service{DB: database, Payments: &payments.Mock{DB: database}, Cfg: rentals.Config{
		ReservationTTL: cfg.ReservationTTL, ProvisionTimeout: cfg.ProvisionTimeout, HeartbeatGrace: cfg.HeartbeatGrace,
	}}
	hub = relay.New(relay.Events{
		OnHello:           machSvc.OnHello,
		OnHeartbeat:       machSvc.OnHeartbeat,
		OnDisconnect:      machSvc.OnDisconnect,
		OnProvisionResult: rentSvc.OnProvisionResult,
		OnTerminateResult: rentSvc.OnTerminateResult,
		OnSandboxStatus:   rentSvc.OnSandboxStatus,
		OnStateReport:     rentSvc.OnStateReport,
	}, cfg.MinAgentProtocol)
	rentSvc.Hub = hub

	go rentSvc.RunSweeper(ctx, 15*time.Second)

	srv := &apihttp.Server{Cfg: cfg, Auth: authSvc, Machines: machSvc, Rentals: rentSvc, Hub: hub}
	hs := &http.Server{Addr: cfg.Addr, Handler: srv.Router(), ReadHeaderTimeout: 10 * time.Second}
	go func() {
		slog.Info("api listening", "addr", cfg.Addr, "public_url", cfg.PublicURL)
		if err := hs.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			slog.Error("listen", "err", err)
			os.Exit(1)
		}
	}()
	<-ctx.Done()
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
	defer cancel()
	_ = hs.Shutdown(shutdownCtx)
}
