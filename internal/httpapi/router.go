package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/analysis"
	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// Deps bundles what the router needs to wire handlers and middleware.
type Deps struct {
	Logger          *slog.Logger
	Installations   *store.Installations
	InstallerSecret string
	JWTAudience     string
	LLM             analysis.LLM
}

// NewRouter builds the http handler tree. Public routes (health, version,
// lifecycle) sit outside the auth middleware; /v1 feature routes live
// behind the Forge-JWT-authenticated middleware.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

	// Public routes.
	mux.HandleFunc("GET /v1/health", handleHealth)
	mux.HandleFunc("GET /v1/version", handleVersion)

	// Lifecycle webhook — authed by pre-shared installer secret, not the
	// per-install JWT middleware (chicken-and-egg: before install there's no
	// per-install secret yet).
	if d.Installations != nil {
		lifecycle := &LifecycleHandler{
			Logger:          d.Logger,
			Installations:   d.Installations,
			InstallerSecret: d.InstallerSecret,
		}
		mux.Handle("POST /v1/installations/lifecycle", lifecycle)
	}

	// Feature routes behind auth middleware.
	authed := http.NewServeMux()
	if d.LLM != nil {
		analysisH := &AnalysisHandler{Logger: d.Logger, LLM: d.LLM}
		authed.Handle("POST /v1/analysis", analysisH)
	}

	if d.Installations != nil {
		mw := auth.Middleware(d.Logger, d.Installations, d.JWTAudience)
		mux.Handle("/v1/", mw(authed))
	}

	return withRequestLogging(d.Logger, mux)
}
