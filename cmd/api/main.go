// Package main is the RideAndConnect API entrypoint.
//
// It loads config, initialises the logger, opens the DB pool, constructs
// the routes Factory (with whichever provider adapters have credentials),
// wires the auth/users/rides services, mounts the GraphQL handler, and
// serves HTTP via chi with the middleware chain from internal/middleware.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/SohanBiswasSB/rideandconnect/internal/audit"
	"github.com/SohanBiswasSB/rideandconnect/internal/auth"
	"github.com/SohanBiswasSB/rideandconnect/internal/config"
	"github.com/SohanBiswasSB/rideandconnect/internal/database"
	"github.com/SohanBiswasSB/rideandconnect/internal/graph"
	"github.com/SohanBiswasSB/rideandconnect/internal/logging"
	"github.com/SohanBiswasSB/rideandconnect/internal/middleware"
	"github.com/SohanBiswasSB/rideandconnect/internal/observability"
	"github.com/SohanBiswasSB/rideandconnect/internal/realtime"
	"github.com/SohanBiswasSB/rideandconnect/internal/rides"
	"github.com/SohanBiswasSB/rideandconnect/internal/routes"
	"github.com/SohanBiswasSB/rideandconnect/internal/routes/google"
	"github.com/SohanBiswasSB/rideandconnect/internal/routes/mapbox"
	"github.com/SohanBiswasSB/rideandconnect/internal/users"
)

// Build-time vars injected via -ldflags.
var (
	version   = "dev"
	commit    = "none"
	buildTime = "unknown"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "rideandconnect: fatal: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	log := logging.New(string(cfg.Env), cfg.LogLevel, os.Stdout)

	if err := cfg.Validate(); err != nil {
		if cfg.Env.IsProd() {
			return fmt.Errorf("config validation: %w", err)
		}
		log.Warn().Err(err).Msg("config: validation failed (non-prod, continuing)")
	}

	rootCtx, rootCancel := context.WithCancel(context.Background())
	defer rootCancel()

	// DB pool. Optional in dev — without it, auth/rides resolvers will
	// fail at first DB call but the meta endpoints stay healthy.
	pool, err := database.Open(rootCtx, cfg.DatabaseURL)
	if err != nil {
		if cfg.Env.IsProd() {
			return fmt.Errorf("database: %w", err)
		}
		log.Warn().Err(err).Msg("database: open failed (non-prod, continuing without db)")
	}
	if pool != nil {
		defer pool.Close()
		database.StartPruneSweeper(rootCtx, pool, log)
	}

	factory, err := buildRoutesFactory(cfg, log)
	if err != nil {
		return fmt.Errorf("routes factory: %w", err)
	}

	middleware.SetTrustProxyHeaders(cfg.TrustProxyHeaders)

	authSvc, usersSvc, ridesSvc := buildDomainServices(pool, cfg)
	auditRec := audit.New(pool, log)
	if authSvc != nil && auditRec != nil {
		authSvc.SetAudit(auditRec)
	}
	if ridesSvc != nil && auditRec != nil {
		ridesSvc.SetAudit(auditRec)
	}
	if ridesSvc != nil {
		ridesSvc.AttachEngine(rootCtx, rides.EngineConfig{
			StaleTTL:     cfg.PeerRouteStaleTTL,
			CacheTTL:     cfg.RouteCacheTTL,
			PeerCacheTTL: cfg.PeerRouteCacheTTL,
			DailyCap:     cfg.RouteProviderDailyCap,
		})
	}
	provider := &activeProvider{factory: factory, name: string(cfg.RouteProvider)}

	metrics := observability.New()

	hub := realtime.NewHub(log)
	hub.SetMetrics(metrics)
	resolver := graph.NewResolver(authSvc, usersSvc, ridesSvc, provider)
	if ridesSvc != nil && hub != nil {
		resolver.SetPresence(presenceAdapter{hub: hub})
		resolver.SetEmergencyHook(hub.BroadcastEmergency)
		resolver.SetDestinationHook(hub.BroadcastDestination)
		resolver.SetRideEndedHook(hub.BroadcastRideEnded)
	}

	gqlHandler, err := graph.NewHandler(resolver, cfg.IntrospectionEnabled())
	if err != nil {
		return fmt.Errorf("graphql handler: %w", err)
	}

	r := chi.NewRouter()
	r.Use(middleware.RequestID())
	r.Use(middleware.Logger(log, metrics))
	r.Use(middleware.Recoverer())
	r.Use(middleware.MaxBodyBytes(64 * 1024))
	r.Use(middleware.GzipRequestBody())
	r.Use(middleware.CORS(cfg.CORSAllowedOrigins))
	r.Use(middleware.RateLimitPerIP(cfg.RateLimitRPS))

	r.Get("/healthz", healthz)
	r.Get("/readyz", readyz(factory, cfg, pool))
	r.Get("/version", versionHandler)
	r.Handle("/metrics", metrics.Handler())

	// GraphQL: auth middleware attaches user_id when a Bearer is present;
	// resolvers gate per-operation. WS upgrade lives at the same prefix.
	r.Route("/api/v1", func(r chi.Router) {
		// Persisted queries layer: rewrites hash-only requests to full
		// queries before they reach the GraphQL handler.
		pq := graph.PersistedQueryMiddleware()
		if authSvc != nil {
			r.With(middleware.RequireAuth(authSvc), pq).Post("/graphql", gqlHandler.ServeHTTP)
		} else {
			r.With(pq).Post("/graphql", gqlHandler.ServeHTTP)
		}
		if authSvc != nil && ridesSvc != nil {
			r.Get("/ws/rides/{rideId}", realtime.UpgradeHandler(hub, authSvc, ridesSvc, usersSvc, log))
		} else {
			r.Get("/ws/rides/{rideId}", wsPlaceholder)
		}
	})

	// HTTP/2 cleartext (h2c) lets the same listener serve HTTP/1.1 and
	// HTTP/2 over plain TCP. HTTP/2's HPACK header compression saves
	// 200–400 B per GraphQL request (Authorization, Content-Type,
	// User-Agent are all repeated and compressible). We don't terminate
	// TLS here — production runs behind Caddy/NGINX which handles
	// HTTPS+ALPN; h2c covers the dev path. WS connections don't
	// negotiate h2 (still HTTP/1.1 Upgrade); h2c.NewHandler routes them
	// through the inner handler unchanged.
	h2s := &http2.Server{
		IdleTimeout:     60 * time.Second,
		ReadIdleTimeout: 60 * time.Second,
	}
	srv := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           h2c.NewHandler(r, h2s),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() {
		log.Info().
			Str("addr", cfg.HTTPAddr).
			Str("version", version).
			Str("commit", commit).
			Bool("introspection", cfg.IntrospectionEnabled()).
			Msg("rideandconnect: listening")
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	select {
	case err := <-errCh:
		return fmt.Errorf("server error: %w", err)
	case sig := <-stop:
		log.Info().Stringer("signal", sig).Msg("rideandconnect: shutdown")
	}

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("graceful shutdown failed")
		return err
	}
	hub.Stop(shutdownCtx)
	return nil
}

// buildRoutesFactory wires whichever adapters have credentials.
func buildRoutesFactory(cfg config.Config, log zerolog.Logger) (*routes.Factory, error) {
	var providers []routes.Provider

	if cfg.GoogleRoutesAPIKey != "" {
		gp, err := google.New(google.Config{APIKey: cfg.GoogleRoutesAPIKey, BaseURL: cfg.GoogleRoutesBaseURL})
		if err != nil {
			return nil, fmt.Errorf("google adapter: %w", err)
		}
		providers = append(providers, gp)
		log.Info().Msg("routes: google adapter wired")
	}
	if cfg.MapboxAccessToken != "" {
		mp, err := mapbox.New(mapbox.Config{
			AccessToken:       cfg.MapboxAccessToken,
			BaseURL:           cfg.MapboxBaseURL,
			ProfileTwoWheeler: cfg.MapboxProfileTwoWheeler,
			ProfileBicycle:    cfg.MapboxProfileBicycle,
			ProfileWalk:       cfg.MapboxProfileWalk,
		})
		if err != nil {
			return nil, fmt.Errorf("mapbox adapter: %w", err)
		}
		providers = append(providers, mp)
		log.Info().Msg("routes: mapbox adapter wired")
	}

	return routes.NewFactory(providers...), nil
}

// buildDomainServices constructs the auth/users/rides services. Tolerates
// a nil pool in dev — the GraphQL handler returns an error on first DB
// call, but the server still boots and meta endpoints stay green.
func buildDomainServices(pool *pgxpool.Pool, cfg config.Config) (*auth.Service, *users.Service, *rides.Service) {
	if pool == nil {
		return nil, nil, nil
	}
	authSvc := auth.NewService(pool, auth.Config{
		JWTSecret:   []byte(orDevSecret(cfg)),
		AccessTTL:   cfg.JWTAccessTTL,
		RefreshTTL:  cfg.JWTRefreshTTL,
		WSTicketTTL: cfg.WSTicketTTL,
	})
	usersSvc := users.NewService(pool)
	ridesSvc := rides.NewService(pool)
	return authSvc, usersSvc, ridesSvc
}

// presenceAdapter bridges realtime.LastLocation → rides.LastLocation so
// the rides package doesn't depend on realtime (and vice versa).
type presenceAdapter struct{ hub *realtime.Hub }

func (a presenceAdapter) LookupPresence(rideID, userID uuid.UUID) (rides.LastLocation, bool) {
	loc, ok := a.hub.LookupPresence(rideID, userID)
	if !ok {
		return rides.LastLocation{}, false
	}
	return rides.LastLocation{Latitude: loc.Latitude, Longitude: loc.Longitude, At: loc.At}, true
}

// activeProvider adapts the routes Factory + active provider name to the
// graph.RouteProviderResolver interface. Returns nil from Active when the
// active adapter is unconfigured so resolvers surface a clean
// UPSTREAM_UNAVAILABLE rather than panicking.
type activeProvider struct {
	factory *routes.Factory
	name    string
}

func (a *activeProvider) Active() routes.Provider {
	if a == nil || a.factory == nil {
		return nil
	}
	primary, err := a.factory.Get(a.name)
	if err != nil {
		return nil
	}
	// Build a MultiProvider that tries the configured primary first and
	// falls back through every other configured adapter. Single-adapter
	// deployments behave identically to before; dual-adapter ones get a
	// transparent fallback when the primary is down.
	others := make([]routes.Provider, 0, 2)
	for _, name := range a.factory.Names() {
		if name == a.name {
			continue
		}
		if p, err := a.factory.Get(name); err == nil {
			others = append(others, p)
		}
	}
	if len(others) == 0 {
		return primary
	}
	return routes.NewMulti(append([]routes.Provider{primary}, others...)...)
}

// orDevSecret returns a stable dev fallback when JWT_SECRET is empty.
// Validate() already refuses to start in prod with a missing secret.
func orDevSecret(cfg config.Config) string {
	if cfg.JWTSecret != "" {
		return cfg.JWTSecret
	}
	return "dev-only-jwt-secret-do-not-use-in-prod-32"
}

func healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

func readyz(factory *routes.Factory, cfg config.Config, pool *pgxpool.Pool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var problems []string
		if _, err := factory.Get(string(cfg.RouteProvider)); err != nil {
			problems = append(problems, "active route provider not configured")
		}
		if pool == nil {
			problems = append(problems, "database not connected")
		} else {
			ctx, cancel := context.WithTimeout(r.Context(), 1*time.Second)
			defer cancel()
			if err := pool.Ping(ctx); err != nil {
				problems = append(problems, "database ping failed")
			}
		}
		if len(problems) > 0 {
			writeJSON(w, http.StatusServiceUnavailable, map[string]any{
				"status":   "unavailable",
				"problems": problems,
			})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	}
}

func versionHandler(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{
		"version":    version,
		"commit":     commit,
		"build_time": buildTime,
	})
}

// wsPlaceholder is replaced with the real WS upgrade in Phase 7.
func wsPlaceholder(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Retry-After", "30")
	writeJSON(w, http.StatusServiceUnavailable, map[string]any{
		"error": map[string]any{
			"code":    "UPSTREAM_UNAVAILABLE",
			"message": "WebSocket endpoint not yet wired (Phase 7 pending).",
		},
	})
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}
