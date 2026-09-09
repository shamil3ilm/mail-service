// Package api holds the HTTP handlers and router wiring.
package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/shamil3ilm/mail-service/internal/auth"
	"github.com/shamil3ilm/mail-service/internal/dnspub"
	"github.com/shamil3ilm/mail-service/internal/events"
	"github.com/shamil3ilm/mail-service/internal/provider"
	"github.com/shamil3ilm/mail-service/internal/rawstore"
	"github.com/shamil3ilm/mail-service/internal/storage"
	"github.com/shamil3ilm/mail-service/web"
)

// Server is the public API surface.
type Server struct {
	Store             storage.Store
	Raw               *rawstore.Store
	Bus               events.Bus
	Auth              *auth.Manager
	Relay             provider.Relay
	DNSPublisher      dnspub.Publisher
	Logger            *slog.Logger
	CloudMode         bool
	AutoVerifyDomains []string // suffixes for local auto-provisioning (e.g. ".test")
}

// Router builds the public HTTP router (dashboard + REST + WS).
func (s *Server) Router() http.Handler {
	r := chi.NewRouter()

	r.Use(middleware.RequestID)
	r.Use(middleware.RealIP)
	r.Use(s.slogRequestLogger)
	r.Use(middleware.Recoverer)

	// Session middleware attaches *User to context if a valid cookie is present.
	// It never blocks; RequireUser gates the routes that need auth.
	if s.Auth != nil {
		r.Use(s.Auth.Middleware)
	}

	// Timeout is scoped to request-response routes only — applying it globally
	// races with the SSE stream on disconnect (both try to write to the
	// already-closed response and net/http logs a "superfluous WriteHeader").
	withTimeout := func(fn func(chi.Router)) func(chi.Router) {
		return func(r chi.Router) {
			r.Use(middleware.Timeout(30 * time.Second))
			fn(r)
		}
	}

	r.Group(withTimeout(func(r chi.Router) {
		r.Get("/healthz", s.healthz)
		r.Get("/readyz", s.readyz)
	}))

	// Dashboard SPA. Serve embedded web/dist at "/" and asset files at
	// "/assets/*". http.ServeFileFS (Go 1.22+) serves a single file without
	// http.FileServer's directory-index redirect quirk (which caused a
	// /index.html → / → /index.html loop when combined with our rewrite).
	webFS := web.FS()
	r.Get("/", func(w http.ResponseWriter, req *http.Request) {
		http.ServeFileFS(w, req, webFS, "index.html")
	})
	r.Get("/assets/*", func(w http.ResponseWriter, req *http.Request) {
		name := strings.TrimPrefix(req.URL.Path, "/assets/")
		if name == "" || strings.Contains(name, "..") {
			http.NotFound(w, req)
			return
		}
		http.ServeFileFS(w, req, webFS, name)
	})

	r.Route("/api/v1", func(r chi.Router) {
		// Public auth surface — login/register are the entry point.
		r.Group(withTimeout(func(r chi.Router) {
			r.Post("/auth/login", s.login)
			r.Post("/auth/logout", s.logout)
			r.Post("/auth/register", s.register)
			r.Get("/auth/me", s.me)
		}))

		// Authenticated app surface.
		r.Group(func(r chi.Router) {
			if s.Auth != nil {
				r.Use(auth.RequireUser)
			}

			// Request-response endpoints get the 30s timeout.
			r.Group(withTimeout(func(r chi.Router) {
				r.Get("/mailboxes", s.listMailboxes)
				r.Post("/mailboxes", s.createMailbox)
				r.Get("/mailboxes/{id}", s.getMailbox)

				r.Get("/messages", s.listMessages)
				r.Get("/messages/{id}", s.getMessage)
				r.Get("/messages/{id}/raw", s.getMessageRaw)
				r.Delete("/messages/{id}", s.deleteMessage)
				r.Get("/messages/{id}/attachments", s.listAttachments)
				r.Get("/messages/{id}/attachments/{aid}", s.getAttachment)
				r.Get("/messages/{id}/content", s.getMessageContent)

				r.Get("/threads", s.listThreads)
				r.Get("/threads/{id}/messages", s.listThreadMessages)
				r.Get("/threads/{id}/labels", s.listThreadLabels)
				r.Post("/threads/{id}/labels/{label_id}", s.applyLabelToThread)
				r.Delete("/threads/{id}/labels/{label_id}", s.removeLabelFromThread)

				r.Get("/labels", s.listLabels)
				r.Post("/labels", s.createLabel)
				r.Delete("/labels/{id}", s.deleteLabel)

				r.Get("/search", s.search)

				r.Post("/emails", s.sendEmail)

				r.Get("/keys", s.listAPIKeys)
				r.Post("/keys", s.createAPIKey)
				r.Delete("/keys/{id}", s.revokeAPIKey)

				r.Get("/domains", s.listDomains)
				r.Post("/domains", s.createDomain)
				r.Post("/domains/{id}/verify", s.verifyDomain)
				r.Delete("/domains/{id}", s.deleteDomain)

				r.Get("/suppressions", s.listSuppressions)
				r.Post("/suppressions", s.createSuppression)
				r.Delete("/suppressions/{address}", s.removeSuppression)
			}))

			// SSE stream stays outside the timeout so long-lived connections
			// aren't racing a 504-writer on disconnect.
			r.Get("/stream", s.stream)
		})
	})

	return r
}

// healthz reports liveness. Never touches external systems.
func (s *Server) healthz(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// readyz reports readiness — used by load balancers before routing traffic.
func (s *Server) readyz(w http.ResponseWriter, r *http.Request) {
	ctx, cancel := context.WithTimeout(r.Context(), 2*time.Second)
	defer cancel()

	if err := s.Store.Ready(ctx); err != nil {
		s.Logger.Warn("readyz: storage not ready", slog.String("err", err.Error()))
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{
			"status": "unready",
			"reason": "storage",
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "ready"})
}

// slogRequestLogger emits one JSON line per request with duration + status.
func (s *Server) slogRequestLogger(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Skip the WebSocket path so we don't log a "0 status" line on
		// long-running connections.
		if r.URL.Path == "/api/v1/stream" {
			next.ServeHTTP(w, r)
			return
		}
		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		start := time.Now()
		defer func() {
			s.Logger.Info("http",
				slog.String("method", r.Method),
				slog.String("path", r.URL.Path),
				slog.Int("status", ww.Status()),
				slog.Int("bytes", ww.BytesWritten()),
				slog.Duration("took", time.Since(start)),
				slog.String("req_id", middleware.GetReqID(r.Context())),
			)
		}()
		next.ServeHTTP(ww, r)
	})
}

func writeJSON(w http.ResponseWriter, code int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(body)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}
