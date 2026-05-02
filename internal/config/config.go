// Package config parses the runtime configuration from environment
// variables into a single typed Config value. Every supported variable
// from docs/ARCHITECTURE.md §6 is represented here.
//
// Required vars are validated at startup. In APP_ENV=prod the app refuses
// to boot if any required value is missing or weak; in dev/staging it
// substitutes development-friendly defaults and logs a warning at the
// caller's discretion.
package config

import (
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// Env enumerates the runtime environments. Behavior diverges meaningfully
// at the boundaries — particularly in Validate and around GraphQL
// introspection — so we type it.
type Env string

const (
	EnvDev     Env = "dev"
	EnvStaging Env = "staging"
	EnvProd    Env = "prod"
)

// IsProd reports whether the deployment is production. Used to gate
// stricter validation, disable GraphQL introspection, and require WSS.
func (e Env) IsProd() bool { return e == EnvProd }

// RouteProviderName names the active routes adapter; see internal/routes.
type RouteProviderName string

const (
	RouteProviderGoogle RouteProviderName = "google"
	RouteProviderMapbox RouteProviderName = "mapbox"
)

// Config is the fully parsed runtime configuration.
type Config struct {
	// ── Runtime ────────────────────────────────────────────────────────
	Env      Env
	HTTPAddr string
	LogLevel string

	// ── Database ───────────────────────────────────────────────────────
	DatabaseURL string

	// ── Auth ───────────────────────────────────────────────────────────
	JWTSecret      string
	JWTAccessTTL   time.Duration
	JWTRefreshTTL  time.Duration
	WSTicketTTL    time.Duration

	// ── GraphQL ────────────────────────────────────────────────────────
	GraphQLDepthLimit      int
	GraphQLComplexityLimit int
	// GraphQLIntrospection: "auto", "on", "off". Auto = on outside prod.
	GraphQLIntrospection string

	// ── Route provider selection + adapter config ─────────────────────
	RouteProvider RouteProviderName

	GoogleRoutesAPIKey  string
	GoogleRoutesBaseURL string

	MapboxAccessToken       string
	MapboxBaseURL           string
	MapboxProfileTwoWheeler string
	MapboxProfileBicycle    string
	MapboxProfileWalk       string

	RouteCacheTTL      time.Duration
	PeerRouteCacheTTL  time.Duration
	PeerRouteStaleTTL  time.Duration

	// ── WebSocket / wire / QoS ────────────────────────────────────────
	WSDefaultCodec        string // "toon" | "json"
	WSPermessageDeflate   bool
	WSPingInterval        time.Duration
	WSPingIntervalLow     time.Duration
	WSPingIntervalCritical time.Duration
	WSIdleTimeout         time.Duration
	WSMaxMsgSize          int
	WSMaxWireBytes        int
	WSTierRTTHighMS       int
	WSTierRTTLowMS        int
	WSTierRTTCriticalMS   int
	WSLocHzHigh           float64
	WSLocHzMedium         float64
	WSLocHzLow            float64
	WSLocHzCritical       float64

	// ── HTTP / CORS / rate limit ──────────────────────────────────────
	HTTPCompression    string
	CORSAllowedOrigins []string
	RateLimitRPS       int

	// ── Privacy / feature flags ───────────────────────────────────────
	LocationPersist bool
}

// Load reads every supported variable from the process environment. It
// returns the parsed Config and an error describing any malformed value.
// It does NOT validate required-in-prod fields — call Validate after.
func Load() (Config, error) {
	c := Config{
		Env:      Env(envOr("APP_ENV", string(EnvDev))),
		HTTPAddr: envOr("HTTP_ADDR", ":8080"),
		LogLevel: envOr("LOG_LEVEL", "info"),

		DatabaseURL: os.Getenv("DATABASE_URL"),

		JWTSecret: os.Getenv("JWT_SECRET"),

		GraphQLIntrospection: envOr("GRAPHQL_INTROSPECTION", "auto"),

		RouteProvider: RouteProviderName(strings.ToLower(envOr("ROUTE_PROVIDER", string(RouteProviderGoogle)))),

		GoogleRoutesAPIKey:  os.Getenv("GOOGLE_ROUTES_API_KEY"),
		GoogleRoutesBaseURL: envOr("GOOGLE_ROUTES_BASE_URL", "https://routes.googleapis.com"),

		MapboxAccessToken:       os.Getenv("MAPBOX_ACCESS_TOKEN"),
		MapboxBaseURL:           envOr("MAPBOX_BASE_URL", "https://api.mapbox.com"),
		MapboxProfileTwoWheeler: envOr("MAPBOX_PROFILE_TWO_WHEELER", "cycling"),
		MapboxProfileBicycle:    envOr("MAPBOX_PROFILE_BICYCLE", "cycling"),
		MapboxProfileWalk:       envOr("MAPBOX_PROFILE_WALK", "walking"),

		WSDefaultCodec:  envOr("WS_DEFAULT_CODEC", "toon"),
		HTTPCompression: envOr("HTTP_COMPRESSION", "gzip,br"),
	}

	c.CORSAllowedOrigins = parseCSV(os.Getenv("CORS_ALLOWED_ORIGINS"))

	type entry struct {
		dst    any
		key    string
		dflt   any
	}

	durations := []entry{
		{&c.JWTAccessTTL, "JWT_ACCESS_TTL", 15 * time.Minute},
		{&c.JWTRefreshTTL, "JWT_REFRESH_TTL", 720 * time.Hour},
		{&c.WSTicketTTL, "WS_TICKET_TTL", 5 * time.Minute},
		{&c.RouteCacheTTL, "ROUTE_CACHE_TTL", 30 * time.Minute},
		{&c.PeerRouteCacheTTL, "PEER_ROUTE_CACHE_TTL", 60 * time.Second},
		{&c.PeerRouteStaleTTL, "PEER_ROUTE_STALE_TTL", 30 * time.Second},
		{&c.WSPingInterval, "WS_PING_INTERVAL", 25 * time.Second},
		{&c.WSPingIntervalLow, "WS_PING_INTERVAL_LOW", 30 * time.Second},
		{&c.WSPingIntervalCritical, "WS_PING_INTERVAL_CRITICAL", 60 * time.Second},
		{&c.WSIdleTimeout, "WS_IDLE_TIMEOUT", 60 * time.Second},
	}
	for _, e := range durations {
		v, err := readDuration(e.key, e.dflt.(time.Duration))
		if err != nil {
			return c, err
		}
		*e.dst.(*time.Duration) = v
	}

	ints := []entry{
		{&c.GraphQLDepthLimit, "GRAPHQL_DEPTH_LIMIT", 8},
		{&c.GraphQLComplexityLimit, "GRAPHQL_COMPLEXITY_LIMIT", 200},
		{&c.WSMaxMsgSize, "WS_MAX_MSG_SIZE", 8192},
		{&c.WSMaxWireBytes, "WS_MAX_WIRE_BYTES", 16384},
		{&c.WSTierRTTHighMS, "WS_TIER_RTT_HIGH_MS", 250},
		{&c.WSTierRTTLowMS, "WS_TIER_RTT_LOW_MS", 600},
		{&c.WSTierRTTCriticalMS, "WS_TIER_RTT_CRITICAL_MS", 1500},
		{&c.RateLimitRPS, "RATE_LIMIT_RPS", 20},
	}
	for _, e := range ints {
		v, err := readInt(e.key, e.dflt.(int))
		if err != nil {
			return c, err
		}
		*e.dst.(*int) = v
	}

	floats := []entry{
		{&c.WSLocHzHigh, "WS_LOC_HZ_HIGH", 2.0},
		{&c.WSLocHzMedium, "WS_LOC_HZ_MEDIUM", 1.0},
		{&c.WSLocHzLow, "WS_LOC_HZ_LOW", 0.5},
		{&c.WSLocHzCritical, "WS_LOC_HZ_CRITICAL", 0.2},
	}
	for _, e := range floats {
		v, err := readFloat(e.key, e.dflt.(float64))
		if err != nil {
			return c, err
		}
		*e.dst.(*float64) = v
	}

	bools := []entry{
		{&c.WSPermessageDeflate, "WS_PERMESSAGE_DEFLATE", true},
		{&c.LocationPersist, "LOCATION_PERSIST", false},
	}
	for _, e := range bools {
		v, err := readBool(e.key, e.dflt.(bool))
		if err != nil {
			return c, err
		}
		*e.dst.(*bool) = v
	}

	return c, nil
}

// Validate enforces required-in-prod constraints. Required values are:
//
//   - JWT_SECRET (≥ 32 bytes) and DATABASE_URL — always.
//   - GOOGLE_ROUTES_API_KEY when ROUTE_PROVIDER=google.
//   - MAPBOX_ACCESS_TOKEN  when ROUTE_PROVIDER=mapbox.
//
// Outside prod, missing values are tolerated but the caller should log a
// warning so dev environments fail loudly rather than silently.
func (c Config) Validate() error {
	var problems []string

	switch c.Env {
	case EnvDev, EnvStaging, EnvProd:
		// ok
	default:
		problems = append(problems, fmt.Sprintf("APP_ENV must be one of dev|staging|prod, got %q", c.Env))
	}

	switch c.RouteProvider {
	case RouteProviderGoogle, RouteProviderMapbox:
		// ok
	default:
		problems = append(problems, fmt.Sprintf("ROUTE_PROVIDER must be one of google|mapbox, got %q", c.RouteProvider))
	}

	if c.WSDefaultCodec != "toon" && c.WSDefaultCodec != "json" {
		problems = append(problems, fmt.Sprintf("WS_DEFAULT_CODEC must be toon|json, got %q", c.WSDefaultCodec))
	}

	if c.GraphQLDepthLimit < 1 || c.GraphQLDepthLimit > 64 {
		problems = append(problems, fmt.Sprintf("GRAPHQL_DEPTH_LIMIT must be in [1, 64], got %d", c.GraphQLDepthLimit))
	}
	if c.GraphQLComplexityLimit < 1 || c.GraphQLComplexityLimit > 100000 {
		problems = append(problems, fmt.Sprintf("GRAPHQL_COMPLEXITY_LIMIT must be in [1, 100000], got %d", c.GraphQLComplexityLimit))
	}

	if c.Env.IsProd() {
		if c.DatabaseURL == "" {
			problems = append(problems, "DATABASE_URL is required in prod")
		}
		if len(c.JWTSecret) < 32 {
			problems = append(problems, "JWT_SECRET must be at least 32 bytes in prod")
		}
		switch c.RouteProvider {
		case RouteProviderGoogle:
			if c.GoogleRoutesAPIKey == "" {
				problems = append(problems, "GOOGLE_ROUTES_API_KEY is required when ROUTE_PROVIDER=google in prod")
			}
		case RouteProviderMapbox:
			if c.MapboxAccessToken == "" {
				problems = append(problems, "MAPBOX_ACCESS_TOKEN is required when ROUTE_PROVIDER=mapbox in prod")
			}
		}
	}

	if len(problems) == 0 {
		return nil
	}
	return fmt.Errorf("%w:\n  - %s", ErrInvalidConfig, strings.Join(problems, "\n  - "))
}

// IntrospectionEnabled resolves the GRAPHQL_INTROSPECTION knob. The
// default ("auto") enables introspection outside prod and disables it in
// prod regardless of value unless explicitly forced to "on".
func (c Config) IntrospectionEnabled() bool {
	v := strings.ToLower(c.GraphQLIntrospection)
	switch v {
	case "on":
		return true
	case "off":
		return false
	default: // auto
		return !c.Env.IsProd()
	}
}

// ─── env helpers ─────────────────────────────────────────────────────────

func envOr(key, dflt string) string {
	if v, ok := os.LookupEnv(key); ok && v != "" {
		return v
	}
	return dflt
}

func readDuration(key string, dflt time.Duration) (time.Duration, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return dflt, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q: %v", ErrInvalidConfig, key, v, err)
	}
	return d, nil
}

func readInt(key string, dflt int) (int, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return dflt, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q: %v", ErrInvalidConfig, key, v, err)
	}
	return n, nil
}

func readFloat(key string, dflt float64) (float64, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return dflt, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("%w: %s=%q: %v", ErrInvalidConfig, key, v, err)
	}
	return f, nil
}

func readBool(key string, dflt bool) (bool, error) {
	v, ok := os.LookupEnv(key)
	if !ok || v == "" {
		return dflt, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("%w: %s=%q: %v", ErrInvalidConfig, key, v, err)
	}
	return b, nil
}

func parseCSV(v string) []string {
	if v == "" {
		return nil
	}
	parts := strings.Split(v, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}

// ErrInvalidConfig wraps any configuration-validation error so callers can
// discriminate via errors.Is.
var ErrInvalidConfig = errors.New("config: invalid")
