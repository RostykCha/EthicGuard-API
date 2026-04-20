package httpapi

import (
	"log/slog"
	"net/http"

	"github.com/ethicguard/ethicguard-api/internal/auth"
	"github.com/ethicguard/ethicguard-api/internal/catalog"
	"github.com/ethicguard/ethicguard-api/internal/jobs"
	"github.com/ethicguard/ethicguard-api/internal/store"
)

// Deps bundles what the router needs to wire handlers and middleware.
// The analysis path now requires a Projects repo, a Jobs repo, a Findings
// repo, a catalog, and a dispatcher; the LLM lives inside the worker, not
// the HTTP layer.
type Deps struct {
	Logger          *slog.Logger
	Installations   *store.Installations
	Projects        *store.Projects
	Jobs            *store.Jobs
	Findings        *store.Findings
	FindingActions  *store.FindingActions
	UserPreferences *store.UserPreferences
	Digests         *store.Digests
	Audit           *store.Audit
	Catalog         *catalog.Catalog
	Dispatcher      jobs.Dispatcher
	InstallerSecret string
	JWTAudience     string
}

// NewRouter builds the http handler tree. Public routes (health, version,
// lifecycle) sit outside the auth middleware; /v1 feature routes live
// behind the Forge-JWT-authenticated middleware.
func NewRouter(d Deps) http.Handler {
	mux := http.NewServeMux()

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
	if d.Projects != nil && d.Jobs != nil && d.Dispatcher != nil {
		authed.Handle("POST /v1/analysis", &AnalysisHandler{
			Logger:     d.Logger,
			Projects:   d.Projects,
			Jobs:       d.Jobs,
			Dispatcher: d.Dispatcher,
		})
	}
	if d.Jobs != nil && d.Findings != nil && d.Catalog != nil {
		authed.Handle("GET /v1/analysis/{jobId}", &AnalysisStatusHandler{
			Logger:   d.Logger,
			Jobs:     d.Jobs,
			Findings: d.Findings,
			Actions:  d.FindingActions,
			Projects: d.Projects,
			Catalog:  d.Catalog,
		})
	}
	if d.Findings != nil && d.FindingActions != nil {
		authed.Handle("POST /v1/findings/{id}/action", &FindingActionHandler{
			Logger:   d.Logger,
			Findings: d.Findings,
			Actions:  d.FindingActions,
			Audit:    d.Audit,
		})
	}
	if d.Projects != nil {
		settings := &ProjectSettingsHandler{
			Logger:   d.Logger,
			Projects: d.Projects,
			Audit:    d.Audit,
		}
		authed.Handle("GET /v1/projects/{projectKey}/settings", settings)
		authed.Handle("PUT /v1/projects/{projectKey}/settings", settings)
	}
	if d.UserPreferences != nil {
		prefs := &PreferencesHandler{
			Logger:          d.Logger,
			UserPreferences: d.UserPreferences,
			Audit:           d.Audit,
		}
		authed.Handle("GET /v1/preferences", prefs)
		authed.Handle("PUT /v1/preferences", prefs)
	}
	if d.Digests != nil && d.Catalog != nil {
		authed.Handle("GET /v1/digests/latest", &DigestsHandler{
			Logger:  d.Logger,
			Digests: d.Digests,
			Catalog: d.Catalog,
		})
	}

	if d.Installations != nil {
		mw := auth.Middleware(d.Logger, d.Installations, d.JWTAudience)
		mux.Handle("/v1/", mw(authed))
	}

	return withRequestLogging(d.Logger, mux)
}
