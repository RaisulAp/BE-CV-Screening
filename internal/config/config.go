package config

import (
	"time"

	"github.com/caarlos0/env/v11"
	"github.com/joho/godotenv"
)

// Config holds every runtime setting, sourced from the environment. Required
// fields (DatabaseURL, JWTSecret) fail loudly at boot if missing — no silent
// defaults for secrets (BACKEND.md §9).
type Config struct {
	DatabaseURL  string        `env:"DATABASE_URL,required"`
	JWTSecret    string        `env:"JWT_SECRET,required"`
	JWTExpiresIn time.Duration `env:"JWT_EXPIRES_IN" envDefault:"168h"`
	AIServiceURL string        `env:"AI_SERVICE_URL" envDefault:"http://localhost:8000"`
	AIMock       bool          `env:"AI_MOCK" envDefault:"false"`
	FrontendURL  string        `env:"FRONTEND_URL" envDefault:"http://localhost:3000"`
	Port         string        `env:"PORT" envDefault:"3001"`
	MaxCVSizeMB  int64         `env:"MAX_CV_SIZE_MB" envDefault:"5"`
	// CookieSecure marks the auth cookie Secure (HTTPS only). Keep false for
	// localhost dev; set true in production.
	CookieSecure bool `env:"COOKIE_SECURE" envDefault:"false"`

	// Resend (email verification). Empty ResendAPIKey = verification disabled,
	// new accounts are auto-verified so registration keeps working with zero
	// friction until this is configured.
	ResendAPIKey   string `env:"RESEND_API_KEY" envDefault:""`
	ResendFromAddr string `env:"RESEND_FROM_EMAIL" envDefault:"onboarding@resend.dev"`

	// Cloudflare Turnstile (CAPTCHA on register). Empty secret = disabled.
	TurnstileSecretKey string `env:"TURNSTILE_SECRET_KEY" envDefault:""`

	// DemoEnabled mounts the no-login /demo/* routes (internal/demo) that hit
	// the AI Service directly for quick testing/demos. Defaults off so it's
	// never accidentally exposed; flip to true only for local/dev use.
	DemoEnabled bool `env:"DEMO_ENABLED" envDefault:"false"`
}

// CookieMaxAge is the auth cookie lifetime in seconds (matches the JWT TTL).
func (c Config) CookieMaxAge() int { return int(c.JWTExpiresIn.Seconds()) }

// Load reads .env (if present) then parses the environment into Config.
func Load() (Config, error) {
	// .env is a dev convenience; absence is not an error (prod uses real env).
	_ = godotenv.Load()

	var cfg Config
	if err := env.Parse(&cfg); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

func (c Config) MaxCVSizeBytes() int64 { return c.MaxCVSizeMB << 20 }
