package server

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"cvscreening/be/internal/analyses"
	"cvscreening/be/internal/auth"
	"cvscreening/be/internal/catalog"
	"cvscreening/be/internal/config"
	"cvscreening/be/internal/demo"
	"cvscreening/be/internal/httpx"
)

// Deps bundles everything the router needs to wire handlers.
type Deps struct {
	Cfg         config.Config
	Log         *slog.Logger
	AuthHandler *auth.Handler
	AnalysisH   *analyses.Handler
	CatalogH    *catalog.Handler
	DemoH       *demo.Handler
}

// NewRouter builds the full chi mux. Every route runs withIdentity (cookie →
// identity if present); POST /analyses hard-gates on a real, logged-in
// identity itself (analyses.Handler.create) rather than a router-level wall.
func NewRouter(d Deps) http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(middleware.Recoverer)
	r.Use(requestLogger(d.Log))
	r.Use(securityHeaders)
	r.Use(cors(d.Cfg.FrontendURL))
	r.Use(withIdentity(d.Cfg.JWTSecret))

	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) {
		httpx.WriteSuccess(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	// Auth: register is throttled much tighter than login — registration is the
	// abuse-relevant action (new accounts = fresh free quota), login is just
	// retrying a password.
	r.Group(func(pub chi.Router) {
		pub.Use(httprate.LimitByIP(5, time.Hour))
		pub.Post("/auth/register", d.AuthHandler.Register)
	})
	r.Group(func(pub chi.Router) {
		pub.Use(httprate.LimitByIP(20, time.Minute))
		pub.Post("/auth/login", d.AuthHandler.Login)
		pub.Post("/auth/verify", d.AuthHandler.VerifyEmail)
	})
	r.Post("/auth/logout", d.AuthHandler.Logout)
	r.Get("/auth/me", d.AuthHandler.Me)
	r.Group(func(pub chi.Router) {
		pub.Use(httprate.LimitByIP(3, time.Hour))
		pub.Post("/auth/resend-verification", d.AuthHandler.ResendVerification)
	})

	// Data routes: work for guest or real users, scoped by cookie identity.
	d.AnalysisH.Routes(r)
	d.CatalogH.Routes(r)

	// Demo routes: no login, no persistence, straight through to the AI
	// Service. Off by default — see config.DemoEnabled — and rate-limited
	// per IP since anyone who can reach these can spend real AI Service time.
	if d.Cfg.DemoEnabled {
		r.Group(func(pub chi.Router) {
			pub.Use(httprate.LimitByIP(20, time.Hour))
			d.DemoH.Routes(pub)
		})
		d.Log.Warn("DEMO_ENABLED=true — /demo/* routes are mounted with no auth")
	}

	return r
}
