package hscontrol

import (
	"cmp"
	"errors"
	"io/fs"
	"net/http"
	"path"
	"strings"

	"github.com/go-chi/chi/v5/middleware"
	"github.com/manoharkakumani/orgscale/hscontrol/adminui"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
)

// adminConsoleCSP allows only the console's own scripts and API calls.
// Profile pictures come from the identity provider, so images may be https.
const adminConsoleCSP = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data: https:; connect-src 'self'; font-src 'self' data:; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// adminConsoleHandler serves the single-page console under /admin/. Paths
// that are not files get index.html, so client-side routes survive a reload.
func adminConsoleHandler() http.HandlerFunc {
	files := adminui.FS()
	fileServer := http.StripPrefix("/admin/", http.FileServerFS(files))

	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", adminConsoleCSP)

		p := strings.TrimPrefix(path.Clean(r.URL.Path), "/admin")
		p = strings.TrimPrefix(p, "/")

		if p != "" && p != "index.html" {
			if _, err := fs.Stat(files, p); err == nil {
				if strings.HasPrefix(p, "assets/") {
					// Vite names assets by content hash.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}

				fileServer.ServeHTTP(w, r)

				return
			} else if !errors.Is(err, fs.ErrNotExist) {
				http.Error(w, "internal error", http.StatusInternalServerError)

				return
			}

			if strings.HasPrefix(p, "assets/") {
				http.NotFound(w, r)

				return
			}
		}

		index, err := fs.ReadFile(files, "index.html")
		if err != nil {
			http.Error(w, "admin console not built", http.StatusNotFound)

			return
		}

		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}
}

// auditAPIMiddleware writes every successful change made through /api to the
// audit log: who (admin session or API key), the method and path. Bodies are
// not recorded; they can hold keys.
func (h *Orgscale) auditAPIMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet || r.Method == http.MethodHead || r.Method == http.MethodOptions {
			next.ServeHTTP(w, r)

			return
		}

		var actor auditActor

		switch {
		case r.Header.Get("Authorization") != "":
			actor = apiKeyActor(r)
		case h.admin != nil:
			if s, _ := h.admin.sessionFromRequest(r); s != nil {
				actor = auditActor{Name: s.Email, Kind: types.AuditActorAdmin}
			}
		}

		ww := middleware.NewWrapResponseWriter(w, r.ProtoMajor)
		next.ServeHTTP(ww, r)

		status := cmp.Or(ww.Status(), http.StatusOK)
		if status >= http.StatusBadRequest || actor.Name == "" {
			return
		}

		_ = h.state.AppendAuditEvent(types.AuditEvent{
			Actor:     actor.Name,
			ActorKind: actor.Kind,
			Action:    "api." + strings.ToLower(r.Method),
			Target:    r.URL.Path,
		})
	})
}
