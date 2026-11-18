package hscontrol

import (
	"cmp"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"net/http"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/coreos/go-oidc/v3/oidc"
	apiv1 "github.com/manoharkakumani/orgscale/hscontrol/api/v1"
	apiv2 "github.com/manoharkakumani/orgscale/hscontrol/api/v2"
	"github.com/manoharkakumani/orgscale/hscontrol/templates"
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
	// AdminRoleMember is anyone else with an orgscale account. A member sees
	// only their own access requests (/admin/api/my/...).
	AdminRoleMember AdminRole = "member"
)

// memberAPIPrefix is the only part of the console API a member may use.
const memberAPIPrefix = "/admin/api/my/"

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

	// subject is the provider's immutable identifier (issuer + sub).
	subject string

	remoteAddr    string
	userAgent     string
	createdAt     time.Time
	lastSeen      time.Time
	persistedSeen time.Time
}

// label names the session's person in the audit log from identifiers that
// cannot be spoofed: the verified email, or else the provider's subject.
// Display names and unverified emails are never used.
func (s *adminSession) label() string {
	if s.roleEmail != "" {
		return s.roleEmail
	}

	return "sub:" + s.subject
}

// adminPendingLogin is a login sent to the identity provider and not back yet.
type adminPendingLogin struct {
	Nonce     string
	Verifier  string
	ExpiresAt time.Time
}

// pendingLogins holds logins in flight, bounded in number and age. It has no
// background goroutine: expired entries are dropped as new ones arrive.
type pendingLogins struct {
	mu sync.Mutex
	m  map[string]adminPendingLogin
}

func (p *pendingLogins) Add(state string, l adminPendingLogin) {
	p.mu.Lock()
	defer p.mu.Unlock()

	now := time.Now()
	l.ExpiresAt = now.Add(adminPendingLoginTTL)

	for k, v := range p.m {
		if now.After(v.ExpiresAt) {
			delete(p.m, k)
		}
	}

	if len(p.m) >= adminPendingLogins {
		var oldest string
		for k, v := range p.m {
			if oldest == "" || v.ExpiresAt.Before(p.m[oldest].ExpiresAt) {
				oldest = k
			}
		}

		delete(p.m, oldest)
	}

	p.m[state] = l
}

// Take returns and removes the login for state, if it has not expired.
func (p *pendingLogins) Take(state string) (adminPendingLogin, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()

	l, ok := p.m[state]
	delete(p.m, state)

	return l, ok && time.Now().Before(l.ExpiresAt)
}

type adminAuth struct {
	h *Orgscale

	pending *pendingLogins

	// loginLimit and setupLimit slow down guessing and floods per client.
	loginLimit *ipLimiter
	setupLimit *ipLimiter

	mu            sync.Mutex
	sessions      map[string]*adminSession // by SHA-256 of the cookie token
	setupSessions map[string]*setupSession // by SHA-256 of the cookie token
}

// newAdminAuth starts the console's sign-in, with the sessions stored in the
// database. The identity provider is looked up on each sign-in, so it can be
// configured later from the setup page.
func newAdminAuth(h *Orgscale) *adminAuth {
	aa := &adminAuth{
		h:             h,
		pending:       &pendingLogins{m: make(map[string]adminPendingLogin)},
		loginLimit:    newIPLimiter(30, time.Minute),
		setupLimit:    newIPLimiter(10, 10*time.Minute),
		sessions:      make(map[string]*adminSession),
		setupSessions: make(map[string]*setupSession),
	}

	rows, err := h.state.ListAdminSessions()
	if err != nil {
		log.Warn().Err(err).Msg("admin console: loading sessions; everyone signs in again")
	}

	for _, rec := range rows {
		aa.sessions[rec.ID] = sessionFromRecord(rec)
	}

	return aa
}

// provider returns the identity provider and the OAuth2 settings for the
// console's own redirect URL, or nil before single sign-on is configured.
func (aa *adminAuth) provider() (*AuthProviderOIDC, *oauth2.Config) {
	p := aa.h.oidcProvider()
	if p == nil {
		return nil, nil
	}

	c := *p.oauth2Config
	c.RedirectURL = strings.TrimSuffix(aa.h.cfg.ServerURL, "/") + adminCallbackPath

	return p, &c
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
	prov, oauthCfg := aa.provider()
	if prov == nil {
		// Not set up yet: the console shows how to finish setup.
		http.Redirect(w, r, "/admin/", http.StatusFound)

		return
	}

	state := rands.HexString(64)
	nonce := rands.HexString(64)

	opts := []oauth2.AuthCodeOption{oidc.Nonce(nonce)}
	pending := adminPendingLogin{Nonce: nonce}

	pkce := prov.cfg.PKCE
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

	for k, v := range prov.cfg.ExtraParams {
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

	http.Redirect(w, r, oauthCfg.AuthCodeURL(state, opts...), http.StatusFound)
}

// CallbackHandler completes a login and starts a session.
func (aa *adminAuth) CallbackHandler(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()

	prov, oauthCfg := aa.provider()
	if prov == nil {
		adminLoginError(w, http.StatusServiceUnavailable, "Single sign-on is not set up yet.")

		return
	}

	state := r.URL.Query().Get("state")
	code := r.URL.Query().Get("code")

	cookie, err := r.Cookie(adminStateCookie)
	if err != nil || state == "" || subtle.ConstantTimeCompare([]byte(cookie.Value), []byte(state)) != 1 {
		adminLoginError(w, http.StatusForbidden, "The sign-in link is invalid or was opened in another browser. Start again from the console.")

		return
	}

	pending, ok := aa.pending.Take(state)

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

	token, err := oauthCfg.Exchange(ctx, code, exchangeOpts...)
	if err != nil {
		log.Warn().Err(err).Msg("admin console: exchanging code")
		adminLoginError(w, http.StatusForbidden, "The identity provider rejected the sign-in.")

		return
	}

	idToken, err := prov.extractIDToken(ctx, token)
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

	userinfo, err := prov.oidcProvider.UserInfo(ctx, oauth2.StaticTokenSource(token))
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

	applyProviderEmail(&claims)

	// The person's orgscale user: the one their devices joined as, or one
	// a directory sync made for them. A disabled user cannot sign in.
	var userID uint

	u, err := aa.h.state.GetUserByOIDCIdentifier(claims.Identifier())
	if err != nil {
		u = provisionedUserForClaim(aa.h.state, &claims, false)
	}

	if u != nil {
		userID = u.ID

		disabled, err := aa.h.state.IsUserDisabled(types.UserID(u.ID))
		if err != nil || disabled {
			adminLoginError(w, http.StatusForbidden, "Your access has been disabled by an administrator.")

			return
		}
	}

	role, ok := aa.roleFor(&claims)
	if !ok && userID != 0 && aa.trustedEmail(&claims) != "" {
		role, ok = AdminRoleMember, true
	}

	if !ok {
		aa.audit(types.AuditEvent{
			Actor: cmp.Or(aa.trustedEmail(&claims), "sub:"+claims.Identifier()), ActorKind: types.AuditActorAdmin,
			Action: "admin.login.denied",
		})
		adminLoginError(w, http.StatusForbidden,
			"You signed in, but you have no orgscale account yet. Connect a device first (it creates your account), or ask an administrator.")

		return
	}

	now := time.Now()
	sessionToken := rands.HexString(adminSessionTokenSize * 2)
	sess := &adminSession{
		remoteAddr: clientIP(r),
		userAgent:  truncateString(r.UserAgent(), 200),
		createdAt:  now,
		lastSeen:   now,

		CSRF:      rands.HexString(64),
		UserID:    userID,
		Email:     claims.Email,
		Name:      cmp.Or(claims.Name, claims.Email, claims.Sub),
		Picture:   claims.ProfilePictureURL,
		Role:      role,
		ExpiresAt: time.Now().Add(aa.h.cfg.Admin.SessionTimeout),

		roleEmail:  aa.trustedEmail(&claims),
		roleGroups: slices.Clone(claims.Groups),
		subject:    claims.Identifier(),
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

	aa.audit(types.AuditEvent{Actor: sess.label(), ActorKind: types.AuditActorAdmin, Action: "admin.login", Detail: string(role)})

	if role == AdminRoleAdmin {
		aa.finishSetup()
	}

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

		aa.dropSessions(key)

		aa.audit(types.AuditEvent{Actor: sess.label(), ActorKind: types.AuditActorAdmin, Action: "admin.logout"})
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

// sessionRole decides a session's role now: admin or viewer from the
// current lists, else member while the session's user exists and is not
// disabled.
func (aa *adminAuth) sessionRole(sess *adminSession) (AdminRole, bool) {
	if role, ok := aa.roleForIdentity(sess.roleEmail, sess.roleGroups); ok {
		return role, true
	}

	if sess.UserID == 0 || sess.roleEmail == "" {
		return "", false
	}

	if _, err := aa.h.state.GetUserByID(types.UserID(sess.UserID)); err != nil {
		return "", false
	}

	disabled, err := aa.h.state.IsUserDisabled(types.UserID(sess.UserID))
	if err != nil || disabled {
		return "", false
	}

	return AdminRoleMember, true
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
	var drop []string

	aa.mu.Lock()

	now := time.Now()
	for k, v := range aa.sessions {
		if now.After(v.ExpiresAt) {
			delete(aa.sessions, k)
			drop = append(drop, k)
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
		drop = append(drop, oldest)
	}

	aa.sessions[key] = s
	rec := sessionRecord(key, s)
	aa.mu.Unlock()

	err := aa.h.state.SaveAdminSession(rec)
	if err != nil {
		log.Warn().Err(err).Msg("admin console: storing session; it ends on restart")
	}

	if len(drop) > 0 {
		_ = aa.h.state.DeleteAdminSessions(drop...)
	}
}

// dropSessions ends sessions by key.
func (aa *adminAuth) dropSessions(keys ...string) {
	aa.mu.Lock()
	for _, k := range keys {
		delete(aa.sessions, k)
	}
	aa.mu.Unlock()

	err := aa.h.state.DeleteAdminSessions(keys...)
	if err != nil {
		log.Warn().Err(err).Msg("admin console: deleting sessions")
	}
}

func truncateString(s string, n int) string {
	if len(s) <= n {
		return s
	}

	return s[:n]
}

// sessionFromRequest returns the live session named by the request's cookie.
func (aa *adminAuth) sessionFromRequest(r *http.Request) (*adminSession, string) {
	c, err := r.Cookie(adminSessionCookie)
	if err != nil || c.Value == "" {
		return nil, ""
	}

	key := hashToken(c.Value)

	aa.mu.Lock()
	s, ok := aa.sessions[key]
	aa.mu.Unlock()

	if !ok {
		return nil, ""
	}

	if time.Now().After(s.ExpiresAt) {
		aa.dropSessions(key)

		return nil, ""
	}

	return s, key
}

// endSessionsOf ends every session of the person with email, as when they
// lose their admin role.
func (aa *adminAuth) endSessionsOf(email string) {
	var drop []string

	aa.mu.Lock()
	for k, s := range aa.sessions {
		if strings.EqualFold(s.roleEmail, email) || strings.EqualFold(s.Email, email) {
			drop = append(drop, k)
		}
	}
	aa.mu.Unlock()

	if len(drop) > 0 {
		aa.dropSessions(drop...)
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
	role, ok := aa.sessionRole(sess)
	if !ok {
		aa.dropSessions(key)

		return nil, http.StatusUnauthorized, errors.New("your console access was removed")
	}

	now := time.Now()
	persist := false

	aa.mu.Lock()
	sess.Role = role
	sess.lastSeen = now

	if now.Sub(sess.persistedSeen) > 5*time.Minute {
		sess.persistedSeen = now
		persist = true
	}
	aa.mu.Unlock()

	if persist {
		_ = aa.h.state.TouchAdminSession(key, now.UTC())
	}

	if subtle.ConstantTimeCompare([]byte(r.Header.Get(adminCSRFHeader)), []byte(sess.CSRF)) != 1 {
		return nil, http.StatusForbidden, errAdminCSRF
	}

	switch role {
	case AdminRoleAdmin:
	case AdminRoleMember:
		if !strings.HasPrefix(r.URL.Path, memberAPIPrefix) {
			return nil, http.StatusForbidden, errors.New("only administrators can use this")
		}
	default:
		if r.Method != http.MethodGet && r.Method != http.MethodHead {
			return nil, http.StatusForbidden, errors.New("your role is read-only")
		}
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

	page := templates.AuthError(templates.AuthErrorResult{
		Title:   "orgscale admin",
		Heading: "No access to the admin console",
		Message: msg,
	})

	_, _ = w.Write([]byte(page.Render()))
}
