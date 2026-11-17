package hscontrol

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	"github.com/hashicorp/golang-lru/v2/expirable"
	apiv1 "github.com/manoharkakumani/orgscale/hscontrol/api/v1"
	apiv2 "github.com/manoharkakumani/orgscale/hscontrol/api/v2"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/rs/zerolog/log"
	"golang.org/x/oauth2"
	"tailscale.com/util/rands"
)

// The admin console signs administrators in through the same OIDC provider
// devices use, with its own redirect URL (/admin/auth/callback). A signed-in
// administrator holds a server-side session named by an HttpOnly, SameSite
// Strict cookie; every API request made with the session must also carry the
// session's CSRF token in the X-Orgscale-CSRF header.

const (
	adminSessionCookie = "orgscale_admin"
	adminStateCookie   = "orgscale_admin_state"
	adminCSRFHeader    = "X-Orgscale-CSRF"

	adminCallbackPath = "/admin/auth/callback"

	adminPendingLogins    = 4096
	adminPendingLoginTTL  = 10 * time.Minute
	adminMaxSessions      = 1024
	adminSessionTokenSize = 32
)

// AdminRole is what a console session may do.
type AdminRole string

const (
	// AdminRoleAdmin may read and change everything.
	AdminRoleAdmin AdminRole = "admin"
	// AdminRoleViewer may read everything and change nothing.
	AdminRoleViewer AdminRole = "viewer"
)

// adminSession is a signed-in administrator.
type adminSession struct {
	CSRF      string
	UserID    uint
	Email     string
	Name      string
	Picture   string
	Role      AdminRole
	ExpiresAt time.Time

	// roleEmail and roleGroups are what the role was decided from at
	// sign-in: the verified email and the IdP groups. The role is decided
	// again from them on every request, so a role taken away in the
	// console ends the session at once.
	roleEmail  string
	roleGroups []string
}

// label names the session's person in the audit log: the email, or the
// name or subject when the identity provider sends no email.
func (s *adminSession) label() string {
	return cmp.Or(s.Email, s.Name)
}

// adminPendingLogin is a login sent to the identity provider and not back yet.
type adminPendingLogin struct {
	Nonce    string
	Verifier string
}

type adminAuth struct {
	h      *Orgscale
	oidc   *AuthProviderOIDC
	oauth2 oauth2.Config

	pending *expirable.LRU[string, adminPendingLogin]

	mu       sync.Mutex
	sessions map[string]*adminSession // by SHA-256 of the cookie token
}

func newAdminAuth(h *Orgscale, provider *AuthProviderOIDC) *adminAuth {
	cfg := *provider.oauth2Config
	cfg.RedirectURL = strings.TrimSuffix(h.cfg.ServerURL, "/") + adminCallbackPath

	return &adminAuth{
		h:        h,
		oidc:     provider,
		oauth2:   cfg,
		pending:  expirable.NewLRU[string, adminPendingLogin](adminPendingLogins, nil, adminPendingLoginTTL),
		sessions: make(map[string]*adminSession),
	}
}

func hashToken(token string) string {
	sum := sha256.Sum256([]byte(token))

	return hex.EncodeToString(sum[:])
}

func (aa *adminAuth) secureCookies(r *http.Request) bool {
	return strings.HasPrefix(aa.h.cfg.ServerURL, "https://") || r.TLS != nil
}

// LoginHandler sends the browser to the identity provider.
func (aa *adminAuth) LoginHandler(w http.ResponseWriter, r *http.Request) {
	state := rands.HexString(64)
	nonce := rands.HexString(64)

	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce)}
	pending := adminPendingLogin{Nonce: nonce}

	pkce := aa.oidc.cfg.PKCE
	if pkce.Enabled {
		pending.Verifier = oauth2.GenerateVerifier()

		switch pkce.Method {
		case types.PKCEMethodPlain:
			opts = append(opts,
				oauth2.SetAuthURLParam("code_challenge_method", "plain"),
				oauth2.SetAuthURLParam("code_challenge", pending.Verifier))
		default:
			opts = append(opts, oauth2.S256ChallengeOption(pending.Verifier))
		}
	}

	for k, v := range aa.oidc.cfg.ExtraParams {
		opts = append(opts, oauth2.SetAuthURLParam(k, v))
	}

	aa.pending.Add(state, pending)

	//nolint:gosec // G124: Secure from server_url scheme; HttpOnly and SameSite set
	http.SetCookie(w, &http.Cookie{
		Name:     adminStateCookie,
		Value:    state,
		Path:     adminCallbackPath,
		MaxAge:   int(adminPendingLoginTTL.Seconds()),
		Secure:   aa.secureCookies(r),
		HttpOnly: true,
		// Lax: the callback is a cross-site top-level GET from the IdP.
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, aa.oauth2.AuthCodeURL(state, opts...), http.StatusFound)
}

// CallbackHandler completes a login and starts a session.
func (aa *adminAuth) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	cookie, err := r.Cookie(adminStateCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		adminLoginError(w, http.StatusForbidden, "The sign-in link is invalid or was opened in another browser. Start again from the console.")

		return
	}

	pending, ok := aa.pending.Get(state)
	aa.pending.Remove(state)

	if !ok {
		adminLoginError(w, http.StatusGone, "The sign-in took too long. Start again from the console.")

		return
	}

	http.SetCookie(w, &http.Cookie{Name: adminStateCookie, Path: adminCallbackPath, MaxAge: -1})

	if r.URL.Query().Get("error") != "" || code == "" {
		adminLoginError(w, http.StatusForbidden, "The identity provider did not sign you in.")

		return
	}

	var exchangeOpts []oauth2.AuthCodeOption
	if pending.Verifier != "" {
		exchangeOpts = append(exchangeOpts, oauth2.VerifierOption(pending.Verifier))
	}

	token, err := aa.oauth2.Exchange(ctx, code, exchangeOpts...)
	if err != nil {
		log.Warn().Err(err).Msg("admin console: exchanging code")
		adminLoginError(w, http.StatusForbidden, "The identity provider rejected the sign-in.")

		return
	}

	idToken, err := aa.oidc.extractIDToken(ctx, token)
	if err != nil {
		log.Warn().Err(err).Msg("admin console: verifying ID token")
		adminLoginError(w, http.StatusForbidden, "The identity provider's token could not be verified.")

		return
	}

	if subtle.ConstantTimeCompare([]byte(idToken.Nonce), []byte(pending.Nonce)) != 1 {
		adminLoginError(w, http.StatusForbidden, "The sign-in could not be verified. Start again from the console.")

		return
	}

	var claims types.OIDCClaims

	err = idToken.Claims(&claims)
	if err != nil {
		adminLoginError(w, http.StatusForbidden, "The identity provider's token could not be read.")

		return
	}

	userinfo, err := aa.oidc.oidcProvider.UserInfo(ctx, oauth2.StaticTokenSource(token))
	if err == nil {
		if ui, ok := userInfoForSubject(userinfo, claims.Sub); ok {
			claims.Email, claims.EmailVerified = mergeEmailClaims(&claims, &ui)
			claims.Name = cmp.Or(ui.Name, claims.Name)
			claims.ProfilePictureURL = cmp.Or(ui.Picture, claims.ProfilePictureURL)

			if ui.Groups != nil {
				claims.Groups = ui.Groups
			}
		}
	}

	role, ok := aa.roleFor(&claims)
	if !ok {
		aa.audit(types.AuditEvent{
			Actor: cmp.Or(claims.Email, claims.Sub), ActorKind: types.AuditActorAdmin,
			Action: "admin.login.denied",
		})
		adminLoginError(w, http.StatusForbidden,
			"You signed in, but you are not an orgscale administrator. Ask an administrator to add you.")

		return
	}

	// A disabled user cannot administer either.
	var userID uint
	if u, err := aa.h.state.GetUserByOIDCIdentifier(claims.Identifier()); err == nil {
		userID = u.ID

		disabled, err := aa.h.state.IsUserDisabled(types.UserID(u.ID))
		if err != nil || disabled {
			adminLoginError(w, http.StatusForbidden, "Your access has been disabled by an administrator.")

			return
		}
	}

	sessionToken := rands.HexString(adminSessionTokenSize * 2)
	sess := &adminSession{
		CSRF:      rands.HexString(64),
		UserID:    userID,
		Email:     claims.Email,
		Name:      cmp.Or(claims.Name, claims.Email, claims.Sub),
		Picture:   claims.ProfilePictureURL,
		Role:      role,
		ExpiresAt: time.Now().Add(aa.h.cfg.Admin.SessionTimeout),

		roleEmail:  aa.trustedEmail(&claims),
		roleGroups: slices.Clone(claims.Groups),
	}

	aa.storeSession(hashToken(sessionToken), sess)

	//nolint:gosec // G124: Secure from server_url scheme; HttpOnly and SameSite set
	http.SetCookie(w, &http.Cookie{
		Name:     adminSessionCookie,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(aa.h.cfg.Admin.SessionTimeout.Seconds()),
		Secure:   aa.secureCookies(r),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	aa.audit(types.AuditEvent{Actor: sess.Email, ActorKind: types.AuditActorAdmin, Action: "admin.login", Detail: string(role)})

	http.Redirect(w, r, "/admin/", http.StatusFound)
}

// LogoutHandler ends the session.
func (aa *adminAuth) LogoutHandler(w http.ResponseWriter, r *http.Request) {
	sess, key := aa.sessionFromRequest(r)
	if sess != nil {
		if r.Header.Get(adminCSRFHeader) != sess.CSRF {
			http.Error(w, "missing CSRF token", http.StatusForbidden)

			return
		}

		aa.mu.Lock()
		delete(aa.sessions, key)
		aa.mu.Unlock()

		aa.audit(types.AuditEvent{Actor: sess.Email, ActorKind: types.AuditActorAdmin, Action: "admin.logout"})
	}

	http.SetCookie(w, &http.Cookie{Name: adminSessionCookie, Path: "/", MaxAge: -1})
	w.WriteHeader(http.StatusNoContent)
}

// roleFor decides the console role of a signed-in person from their email
// and groups: admin.users and admin.groups from the configuration file and
// the console, then the viewer lists. Emails count only when verified, or
// when oidc.email_verified_required is off.
func (aa *adminAuth) roleFor(claims *types.OIDCClaims) (AdminRole, bool) {
	return aa.roleForIdentity(aa.trustedEmail(claims), claims.Groups)
}

// trustedEmail is the claim's email when it may decide a role: only when the
// identity provider marks it verified, whatever oidc.email_verified_required
// says, since an unverified email could be anyone's.
func (aa *adminAuth) trustedEmail(claims *types.OIDCClaims) string {
	if claims.Email != "" && bool(claims.EmailVerified) {
		return strings.ToLower(claims.Email)
	}

	return ""
}

func (aa *adminAuth) roleForIdentity(email string, groups []string) (AdminRole, bool) {
	list := func(key string, fromConfig []string) []string {
		var v []string

		_, err := aa.h.state.GetSettingJSON(key, &v)
		if err != nil {
			log.Warn().Err(err).Str("setting", key).Msg("admin console: unreadable role setting")
		}

		return append(slices.Clone(fromConfig), v...)
	}

	// matches reports whether the person (email, groups) is in wantUsers
	// or a member of one of wantGroups.
	matches := func(wantUsers, wantGroups []string) bool {
		if email != "" && slices.ContainsFunc(wantUsers, func(u string) bool { return strings.EqualFold(u, email) }) {
			return true
		}

		return slices.ContainsFunc(groups, func(g string) bool {
			return slices.ContainsFunc(wantGroups, func(want string) bool { return strings.EqualFold(want, g) })
		})
	}

	if matches(list(types.SettingAdminUsers, aa.h.cfg.Admin.Users), list(types.SettingAdminGroups, aa.h.cfg.Admin.Groups)) {
		return AdminRoleAdmin, true
	}

	if matches(list(types.SettingViewerUsers, nil), list(types.SettingViewerGroups, nil)) {
		return AdminRoleViewer, true
	}

	return "", false
}

func (aa *adminAuth) storeSession(key string, s *adminSession) {
	aa.mu.Lock()
	defer aa.mu.Unlock()

	now := time.Now()
	for k, v := range aa.sessions {
		if now.After(v.ExpiresAt) {
			delete(aa.sessions, k)
		}
	}

	// Bound memory: drop the session closest to expiry.
	if len(aa.sessions) >= adminMaxSessions {
		var oldest string

		for k, v := range aa.sessions {
			if oldest == "" || v.ExpiresAt.Before(aa.sessions[oldest].ExpiresAt) {
				oldest = k
			}
		}

		delete(aa.sessions, oldest)
	}

	aa.sessions[key] = s
}

// sessionFromRequest returns the live session named by the request's cookie.
func (aa *adminAuth) sessionFromRequest(r *http.Request) (*adminSession, string) {
	c, err := r.Cookie(adminSessionCookie)
	if err != nil || c.Value == "" {
		return nil, ""
	}

	key := hashToken(c.Value)

	aa.mu.Lock()
	defer aa.mu.Unlock()

	s, ok := aa.sessions[key]
	if !ok {
		return nil, ""
	}

	if time.Now().After(s.ExpiresAt) {
		delete(aa.sessions, key)

		return nil, ""
	}

	return s, key
}

// endSessionsOf ends every session of the person with email, as when they
// lose their admin role.
func (aa *adminAuth) endSessionsOf(email string) {
	aa.mu.Lock()
	defer aa.mu.Unlock()

	for k, s := range aa.sessions {
		if strings.EqualFold(s.Email, email) {
			delete(aa.sessions, k)
		}
	}
}

type adminActorKey struct{}

// auditActor names who made a request, for the audit log.
type auditActor struct {
	Name string
	Kind string
}

func withAuditActor(ctx context.Context, a auditActor) context.Context {
	return context.WithValue(ctx, adminActorKey{}, a)
}

func auditActorFrom(ctx context.Context) (auditActor, bool) {
	a, ok := ctx.Value(adminActorKey{}).(auditActor)

	return a, ok
}

var errAdminCSRF = errors.New("missing or wrong " + adminCSRFHeader + " header")

// authenticate checks a console request: a live session, the CSRF header,
// and a role that allows the method.
func (aa *adminAuth) authenticate(r *http.Request) (*adminSession, int, error) {
	sess, key := aa.sessionFromRequest(r)
	if sess == nil {
		return nil, http.StatusUnauthorized, errors.New("not signed in")
	}

	// Decide the role again: the admin and viewer lists may have changed
	// since sign-in.
	role, ok := aa.roleForIdentity(sess.roleEmail, sess.roleGroups)
	if !ok {
		aa.mu.Lock()
		delete(aa.sessions, key)
		aa.mu.Unlock()

		return nil, http.StatusUnauthorized, errors.New("your console access was removed")
	}

	if role != sess.Role {
		aa.mu.Lock()
		sess.Role = role
		aa.mu.Unlock()
	}

	if subtle.ConstantTimeCompare([]byte(r.Header.Get(adminCSRFHeader)), []byte(sess.CSRF)) != 1 {
		return nil, http.StatusForbidden, errAdminCSRF
	}

	if role != AdminRoleAdmin && r.Method != http.MethodGet && r.Method != http.MethodHead {
		return nil, http.StatusForbidden, errors.New("your role is read-only")
	}

	return sess, 0, nil
}

// APIMiddleware lets a console session call the /api/v1 and /api/v2 APIs:
// a request without an Authorization header but with a valid session and
// CSRF header is let through as trusted. Requests with an Authorization
// header are left to the APIs' own key checks.
func (aa *adminAuth) APIMiddleware(next http.Handler) http.Handler {
	trusted := apiv1.WithLocalTrust(apiv2.WithLocalTrust(next))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "" {
			next.ServeHTTP(w, r.WithContext(withAuditActor(r.Context(), apiKeyActor(r))))

			return
		}

		if _, err := r.Cookie(adminSessionCookie); err != nil {
			next.ServeHTTP(w, r)

			return
		}

		sess, code, err := aa.authenticate(r)
		if err != nil {
			http.Error(w, err.Error(), code)

			return
		}

		trusted.ServeHTTP(w, r.WithContext(withAuditActor(r.Context(),
			auditActor{Name: sess.label(), Kind: types.AuditActorAdmin})))
	})
}

// apiKeyActor names an API-key caller by the key's public prefix.
func apiKeyActor(r *http.Request) auditActor {
	h := r.Header.Get("Authorization")
	tok := strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(h, "Bearer "), "Basic "))

	// oskey-api-<prefix>-<secret>: keep only the public part.
	parts := strings.SplitN(tok, "-", 4)
	if len(parts) == 4 {
		return auditActor{Name: strings.Join(parts[:3], "-") + "-***", Kind: types.AuditActorAPIKey}
	}

	return auditActor{Name: "api key", Kind: types.AuditActorAPIKey}
}

func (aa *adminAuth) audit(ev types.AuditEvent) {
	err := aa.h.state.AppendAuditEvent(ev)
	if err != nil {
		log.Error().Err(err).Str("action", ev.Action).Msg("writing audit event")
	}
}

func adminLoginError(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(code)

	_, _ = fmt.Fprintf(w, `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>orgscale admin</title>
<style>body{font-family:system-ui,sans-serif;max-width:32rem;margin:15vh auto;padding:0 1rem;color:#1f2937}a{color:#2563eb}</style></head>
<body><h1>Sign-in failed</h1><p>%s</p><p><a href="/admin/">Back to the console</a></p></body></html>`, htmlEscape(msg))
}

func htmlEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;", `"`, "&quot;", "'", "&#39;")

	return r.Replace(s)
}
