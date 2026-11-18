package hscontrol

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/rs/zerolog/log"
	"tailscale.com/util/rands"
)

// First-time setup, single sign-on configured from the console, and the
// transport rules the console relies on.
//
// A fresh installation has no identity provider, so nobody can sign in to
// the console. Instead of a built-in admin password (a permanent backdoor),
// orgscale writes a one-time setup link to its log. The link is valid for
// 30 minutes and works once; `orgscale admin setup-link`, run on the server,
// makes a new one. The setup page configures single sign-on and names the
// first admin by email; once that admin signs in through the identity
// provider, setup closes.

const (
	setupTokenTTL      = 30 * time.Minute
	setupSessionTTL    = 30 * time.Minute
	setupSessionCookie = "orgscale_setup"
	setupPath          = "/admin/setup"
)

// ---------- switching the identity provider at runtime ----------

// authSwitch is the server's AuthProvider: the OIDC provider once single
// sign-on is configured, the CLI-approval web flow until then. Setting up
// single sign-on in the console swaps it without a restart.
type authSwitch struct {
	web  AuthProvider
	oidc atomic.Pointer[AuthProviderOIDC]
}

func (s *authSwitch) current() AuthProvider {
	if p := s.oidc.Load(); p != nil {
		return p
	}

	return s.web
}

func (s *authSwitch) RegisterHandler(w http.ResponseWriter, r *http.Request) {
	s.current().RegisterHandler(w, r)
}

func (s *authSwitch) AuthHandler(w http.ResponseWriter, r *http.Request) {
	s.current().AuthHandler(w, r)
}

func (s *authSwitch) RegisterURL(id types.AuthID) string { return s.current().RegisterURL(id) }
func (s *authSwitch) AuthURL(id types.AuthID) string     { return s.current().AuthURL(id) }

// oidcProvider returns the live OIDC provider, or nil before single sign-on
// is configured.
func (h *Orgscale) oidcProvider() *AuthProviderOIDC {
	switch p := h.authProvider.(type) {
	case *AuthProviderOIDC:
		return p
	case *authSwitch:
		return p.oidc.Load()
	}

	return nil
}

// setOIDCProvider makes p the live OIDC provider.
func (h *Orgscale) setOIDCProvider(p *AuthProviderOIDC) {
	sw, ok := h.authProvider.(*authSwitch)
	if !ok {
		h.authProvider = p

		return
	}

	if old := sw.oidc.Swap(p); old != nil && old != p {
		old.authCache.Close()
	}
}

// oidcHandler serves a route of the OIDC provider, or 404 before single
// sign-on is configured.
func (h *Orgscale) oidcHandler(pick func(*AuthProviderOIDC) http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p := h.oidcProvider()
		if p == nil {
			http.NotFound(w, r)

			return
		}

		pick(p)(w, r)
	}
}

// storedOIDCConfig is the configuration file's OIDC settings with the
// console's provider, client and scope.
func storedOIDCConfig(base types.OIDCConfig, s types.StoredOIDC) *types.OIDCConfig {
	c := base
	c.Issuer = types.HTTPURL(s.Issuer)
	c.ClientID = s.ClientID

	if len(s.Scope) > 0 {
		c.Scope = slices.Clone(s.Scope)
	}

	return &c
}

// ---------- transport ----------

// consoleTransportOK reports whether the console may be served: over HTTPS,
// on localhost, or with admin.allow_insecure_http.
func consoleTransportOK(cfg *types.Config) bool {
	if cfg.Admin.AllowInsecureHTTP {
		return true
	}

	u, err := url.Parse(cfg.ServerURL)
	if err != nil {
		return false
	}

	if u.Scheme == "https" {
		return true
	}

	host := u.Hostname()
	if host == "localhost" {
		return true
	}

	ip := net.ParseIP(host)

	return ip != nil && ip.IsLoopback()
}

// insecureConsoleHandler explains why the console is not served.
func insecureConsoleHandler(w http.ResponseWriter, _ *http.Request) {
	adminLoginError(w, http.StatusForbidden,
		"The admin console needs HTTPS. Set server_url to an https:// address (or use ORGSCALE_TLS_ACME_HOSTNAME, or a TLS proxy in front). "+
			"For a lab only, set admin.allow_insecure_http (ORGSCALE_ADMIN_ALLOW_INSECURE_HTTP=true).")
}

// hsts asks browsers to use HTTPS only, when the server is reached over it.
func (h *Orgscale) hsts(next http.Handler) http.Handler {
	on := strings.HasPrefix(h.cfg.ServerURL, "https://")

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if on || r.TLS != nil {
			w.Header().Set("Strict-Transport-Security", "max-age=31536000")
		}

		next.ServeHTTP(w, r)
	})
}

// ---------- rate limiting ----------

// ipLimiter allows max requests per window from each client address.
type ipLimiter struct {
	max    int
	window time.Duration

	mu   sync.Mutex
	hits map[string]*ipWindow
}

type ipWindow struct {
	start time.Time
	n     int
}

func newIPLimiter(maxRequests int, window time.Duration) *ipLimiter {
	return &ipLimiter{max: maxRequests, window: window, hits: make(map[string]*ipWindow)}
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}

	return host
}

// note counts a request from ip and reports whether it is within the limit.
func (l *ipLimiter) note(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := time.Now()

	if len(l.hits) > 10000 {
		for k, w := range l.hits {
			if now.Sub(w.start) > l.window {
				delete(l.hits, k)
			}
		}
	}

	w, ok := l.hits[ip]
	if !ok || now.Sub(w.start) > l.window {
		w = &ipWindow{start: now}
		l.hits[ip] = w
	}

	w.n++

	return w.n <= l.max
}

// blocked reports whether ip is over the limit, without counting.
func (l *ipLimiter) blocked(ip string) bool {
	l.mu.Lock()
	defer l.mu.Unlock()

	w, ok := l.hits[ip]

	return ok && time.Since(w.start) <= l.window && w.n >= l.max
}

func tooManyRequests(w http.ResponseWriter, window time.Duration) {
	w.Header().Set("Retry-After", fmt.Sprint(int(window.Seconds())))
	http.Error(w, "too many requests; try again later", http.StatusTooManyRequests)
}

func (l *ipLimiter) middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !l.note(clientIP(r)) {
			tooManyRequests(w, l.window)

			return
		}

		next.ServeHTTP(w, r)
	})
}

// ---------- the setup link ----------

// setupLinkFor returns the URL that opens setup with token. The token is in
// the fragment, so it never reaches proxy or access logs.
func (aa *adminAuth) setupLinkFor(token string) string {
	return strings.TrimSuffix(aa.h.cfg.ServerURL, "/") + setupPath + "#" + token
}

// newSetupLink replaces any pending setup link with a new one.
func (aa *adminAuth) newSetupLink() (string, time.Time, error) {
	token := "oskey-setup-" + rands.HexString(48)
	exp := time.Now().Add(setupTokenTTL).UTC()

	err := aa.h.state.SetSettingJSON(types.SettingSetupToken, types.SetupToken{Hash: hashToken(token), ExpiresAt: exp}, "orgscale")
	if err != nil {
		return "", time.Time{}, err
	}

	aa.audit(types.AuditEvent{Actor: "server", ActorKind: types.AuditActorSystem, Action: "setup.link_created", Detail: "expires " + exp.Format(time.RFC3339)})

	return aa.setupLinkFor(token), exp, nil
}

// announceSetup writes a setup link to the log when single sign-on is not
// configured yet.
func (aa *adminAuth) announceSetup() {
	if aa.h.oidcProvider() != nil {
		return
	}

	link, exp, err := aa.newSetupLink()
	if err != nil {
		log.Error().Err(err).Msg("admin console: creating the setup link")

		return
	}

	log.Warn().Msgf(
		"\n\n  orgscale is not set up yet. To finish setup, open\n\n      %s\n\n"+
			"  within 30 minutes (until %s). The link works once. On the server,\n"+
			"  `orgscale admin setup-link` makes a new one.\n",
		link, exp.Local().Format("15:04 MST"))
}

// SetupLinkHandler makes a new setup link. It is served only on the local
// unix socket, so only someone with access to the server can call it.
func (aa *adminAuth) SetupLinkHandler(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)

		return
	}

	link, exp, err := aa.newSetupLink()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)

		return
	}

	adminJSON(w, http.StatusOK, map[string]any{"url": link, "expiresAt": exp})
}

// setupSession is a browser that opened a valid setup link.
type setupSession struct {
	CSRF      string
	ExpiresAt time.Time
}

func (aa *adminAuth) setupRoutes(r chi.Router) {
	r.With(aa.setupLimit.middleware).Post("/session", aa.handleSetupSession)
	r.Get("/state", aa.requireSetup(aa.handleSetupState))
	r.Put("/sso", aa.requireSetup(aa.handleSetupSSO))
}

// handleSetupSession exchanges a setup link's token for a setup session.
func (aa *adminAuth) handleSetupSession(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Token string `json:"token"`
	}

	if !decodeJSON(w, r, &req) {
		return
	}

	var tok types.SetupToken

	found, err := aa.h.state.GetSettingJSON(types.SettingSetupToken, &tok)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	if !found || time.Now().After(tok.ExpiresAt) {
		adminJSONError(w, http.StatusGone, "This setup link has expired or was already used. On the server, run: orgscale admin setup-link")

		return
	}

	if subtle.ConstantTimeCompare([]byte(hashToken(req.Token)), []byte(tok.Hash)) != 1 {
		aa.audit(types.AuditEvent{Actor: clientIP(r), ActorKind: types.AuditActorSystem, Action: "setup.link_rejected"})
		adminJSONError(w, http.StatusForbidden, "This setup link is not valid.")

		return
	}

	// Single use.
	err = aa.h.state.DeleteSetting(types.SettingSetupToken)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	token := rands.HexString(64)
	sess := &setupSession{CSRF: rands.HexString(64), ExpiresAt: time.Now().Add(setupSessionTTL)}

	aa.mu.Lock()
	aa.setupSessions[hashToken(token)] = sess
	aa.mu.Unlock()

	//nolint:gosec // G124: Secure from server_url scheme; HttpOnly and SameSite set
	http.SetCookie(w, &http.Cookie{
		Name:     setupSessionCookie,
		Value:    token,
		Path:     setupPath,
		MaxAge:   int(setupSessionTTL.Seconds()),
		Secure:   aa.secureCookies(r),
		HttpOnly: true,
		SameSite: http.SameSiteStrictMode,
	})

	aa.audit(types.AuditEvent{Actor: clientIP(r), ActorKind: types.AuditActorSystem, Action: "setup.started"})
	adminJSON(w, http.StatusOK, map[string]string{"csrfToken": sess.CSRF})
}

// requireSetup lets a request through with a live setup session and its
// CSRF token.
func (aa *adminAuth) requireSetup(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		c, err := r.Cookie(setupSessionCookie)
		if err != nil {
			adminJSONError(w, http.StatusUnauthorized, "open the setup link again")

			return
		}

		key := hashToken(c.Value)

		aa.mu.Lock()
		sess, ok := aa.setupSessions[key]

		if ok && time.Now().After(sess.ExpiresAt) {
			delete(aa.setupSessions, key)

			ok = false
		}
		aa.mu.Unlock()

		if !ok {
			adminJSONError(w, http.StatusUnauthorized, "this setup session has ended; open a new setup link")

			return
		}

		if subtle.ConstantTimeCompare([]byte(r.Header.Get(adminCSRFHeader)), []byte(sess.CSRF)) != 1 {
			adminJSONError(w, http.StatusForbidden, errAdminCSRF.Error())

			return
		}

		next(w, r.WithContext(withAuditActor(r.Context(), auditActor{Name: "setup link", Kind: types.AuditActorSystem})))
	}
}

// ---------- single sign-on settings ----------

type ssoState struct {
	ServerURL    string   `json:"serverUrl"`
	RedirectURLs []string `json:"redirectUrls"`
	Configured   bool     `json:"configured"`
	FromConfig   bool     `json:"fromConfig"`
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"clientId"`
	HasSecret    bool     `json:"hasSecret"`
	Scope        []string `json:"scope"`
}

func (aa *adminAuth) ssoState() ssoState {
	base := strings.TrimSuffix(aa.h.cfg.ServerURL, "/")
	st := ssoState{
		ServerURL:    aa.h.cfg.ServerURL,
		RedirectURLs: []string{base + "/oidc/callback", base + adminCallbackPath},
		Configured:   aa.h.oidcProvider() != nil,
		FromConfig:   aa.h.cfg.OIDC.Issuer != "",
		Scope:        slices.Clone(aa.h.cfg.OIDC.Scope),
	}

	if st.FromConfig {
		st.Issuer, st.ClientID, st.HasSecret = string(aa.h.cfg.OIDC.Issuer), aa.h.cfg.OIDC.ClientID, true

		return st
	}

	var stored types.StoredOIDC
	if ok, _ := aa.h.state.GetSettingJSON(types.SettingSSOOIDC, &stored); ok {
		st.Issuer, st.ClientID, st.HasSecret = stored.Issuer, stored.ClientID, stored.ClientSecret != ""
		if len(stored.Scope) > 0 {
			st.Scope = stored.Scope
		}
	}

	return st
}

func (aa *adminAuth) handleSetupState(w http.ResponseWriter, _ *http.Request) {
	adminJSON(w, http.StatusOK, aa.ssoState())
}

// ssoRequest is single sign-on settings from the setup page or Settings.
type ssoRequest struct {
	Issuer       string   `json:"issuer"`
	ClientID     string   `json:"clientId"`
	ClientSecret string   `json:"clientSecret"`
	Scope        []string `json:"scope"`
	// AdminEmail is the first admin, from the setup page only.
	AdminEmail string `json:"adminEmail,omitempty"`
}

var errSSOFromConfig = errors.New("single sign-on is set in the configuration file (oidc.*); change it there")

// applySSO checks single sign-on settings against the identity provider,
// stores them and switches to them.
func (aa *adminAuth) applySSO(ctx context.Context, req ssoRequest, by string) error {
	if aa.h.cfg.OIDC.Issuer != "" {
		return errSSOFromConfig
	}

	req.Issuer = strings.TrimSuffix(strings.TrimSpace(req.Issuer), "/")
	req.ClientID = strings.TrimSpace(req.ClientID)
	req.ClientSecret = strings.TrimSpace(req.ClientSecret)

	u, err := url.Parse(req.Issuer)
	if err != nil || u.Host == "" {
		return errors.New("the issuer must be a URL, e.g. https://accounts.google.com")
	}

	if u.Scheme != "https" {
		ip := net.ParseIP(u.Hostname())
		if u.Scheme != "http" || (u.Hostname() != "localhost" && (ip == nil || !ip.IsLoopback())) {
			return errors.New("the issuer must use https://")
		}
	}

	if req.ClientID == "" {
		return errors.New("enter the client ID from your identity provider")
	}

	var prev types.StoredOIDC

	_, err = aa.h.state.GetSettingJSON(types.SettingSSOOIDC, &prev)
	if err != nil {
		return err
	}

	if req.ClientSecret == "" {
		if prev.ClientSecret == "" || prev.ClientID != req.ClientID || prev.Issuer != req.Issuer {
			return errors.New("enter the client secret from your identity provider")
		}

		req.ClientSecret = prev.ClientSecret
	}

	scope := req.Scope
	if len(scope) == 0 {
		scope = []string{"openid", "profile", "email"}
	}

	if !slices.Contains(scope, "openid") {
		scope = append([]string{"openid"}, scope...)
	}

	stored := types.StoredOIDC{Issuer: req.Issuer, ClientID: req.ClientID, ClientSecret: req.ClientSecret, Scope: scope, UpdatedBy: by}

	dctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	provider, err := NewAuthProviderOIDC(dctx, aa.h, aa.h.cfg.ServerURL, storedOIDCConfig(aa.h.cfg.OIDC, stored), stored.ClientSecret)
	if err != nil {
		return fmt.Errorf("could not reach the identity provider at %s: %w", req.Issuer, err)
	}

	err = aa.h.state.SetSettingJSON(types.SettingSSOOIDC, stored, by)
	if err != nil {
		provider.authCache.Close()

		return err
	}

	aa.h.setOIDCProvider(provider)

	return nil
}

func (aa *adminAuth) handleSetupSSO(w http.ResponseWriter, r *http.Request) {
	var req ssoRequest

	if !decodeJSON(w, r, &req) {
		return
	}

	req.AdminEmail = strings.ToLower(strings.TrimSpace(req.AdminEmail))
	if !validDirectoryEmail(req.AdminEmail) {
		adminJSONError(w, http.StatusBadRequest, "enter the email address of the first administrator")

		return
	}

	err := aa.applySSO(r.Context(), req, "setup link")
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	var admins []string

	_, err = aa.h.state.GetSettingJSON(types.SettingAdminUsers, &admins)
	if err == nil && !slices.ContainsFunc(admins, func(a string) bool { return strings.EqualFold(a, req.AdminEmail) }) {
		err = aa.h.state.SetSettingJSON(types.SettingAdminUsers, append(admins, req.AdminEmail), "setup link")
	}

	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "setup.sso", req.Issuer, "first admin: "+req.AdminEmail)
	adminJSON(w, http.StatusOK, map[string]string{"loginUrl": "/admin/auth/login"})
}

// handleGetSSO and handlePutSSO let admins change single sign-on later.
func (aa *adminAuth) handleGetSSO(w http.ResponseWriter, _ *http.Request) {
	adminJSON(w, http.StatusOK, aa.ssoState())
}

func (aa *adminAuth) handlePutSSO(w http.ResponseWriter, r *http.Request) {
	var req ssoRequest

	if !decodeJSON(w, r, &req) {
		return
	}

	req.AdminEmail = ""

	err := aa.applySSO(r.Context(), req, sessionFrom(r).label())
	if errors.Is(err, errSSOFromConfig) {
		adminJSONError(w, http.StatusConflict, err.Error())

		return
	}

	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	aa.auditReq(r, "settings.sso.update", req.Issuer, "client "+req.ClientID)
	adminJSON(w, http.StatusOK, aa.ssoState())
}

// finishSetup closes setup once an admin has signed in through single
// sign-on: the setup link and setup sessions stop working.
func (aa *adminAuth) finishSetup() {
	aa.mu.Lock()
	n := len(aa.setupSessions)
	clear(aa.setupSessions)
	aa.mu.Unlock()

	var tok types.SetupToken

	found, _ := aa.h.state.GetSettingJSON(types.SettingSetupToken, &tok)
	if found {
		_ = aa.h.state.DeleteSetting(types.SettingSetupToken)
	}

	if n > 0 || found {
		aa.audit(types.AuditEvent{Actor: "server", ActorKind: types.AuditActorSystem, Action: "setup.finished"})
	}
}

// ---------- console sessions ----------

type adminSessionInfo struct {
	ID         string    `json:"id"`
	Email      string    `json:"email"`
	Name       string    `json:"name"`
	CreatedAt  time.Time `json:"createdAt"`
	LastSeenAt time.Time `json:"lastSeenAt"`
	ExpiresAt  time.Time `json:"expiresAt"`
	RemoteAddr string    `json:"remoteAddr"`
	UserAgent  string    `json:"userAgent"`
	Current    bool      `json:"current"`
}

// sessionPublicID names a session in the API without revealing its key.
func sessionPublicID(key string) string { return key[:16] }

func (aa *adminAuth) handleListSessions(w http.ResponseWriter, r *http.Request) {
	me := sessionFrom(r)
	_, myKey := aa.sessionFromRequest(r)

	aa.mu.Lock()
	out := make([]adminSessionInfo, 0, len(aa.sessions))

	for k, s := range aa.sessions {
		if me.Role != AdminRoleAdmin && s.label() != me.label() {
			continue
		}

		out = append(out, adminSessionInfo{
			ID: sessionPublicID(k), Email: s.label(), Name: s.Name,
			CreatedAt: s.createdAt, LastSeenAt: s.lastSeen, ExpiresAt: s.ExpiresAt,
			RemoteAddr: s.remoteAddr, UserAgent: s.userAgent, Current: k == myKey,
		})
	}
	aa.mu.Unlock()

	slices.SortFunc(out, func(a, b adminSessionInfo) int { return b.LastSeenAt.Compare(a.LastSeenAt) })

	adminJSON(w, http.StatusOK, out)
}

// handleEndSession signs a session out; "others" signs out every session of
// the caller except the current one.
func (aa *adminAuth) handleEndSession(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	me := sessionFrom(r)
	_, myKey := aa.sessionFromRequest(r)

	var (
		drop  []string
		names []string
	)

	aa.mu.Lock()
	for k, s := range aa.sessions {
		switch {
		case id == "others" && k != myKey && s.label() == me.label():
		case id != "others" && sessionPublicID(k) == id:
		default:
			continue
		}

		drop = append(drop, k)
		names = append(names, s.label())
	}
	aa.mu.Unlock()

	if len(drop) == 0 && id != "others" {
		adminJSONError(w, http.StatusNotFound, "no such session")

		return
	}

	aa.dropSessions(drop...)
	aa.auditReq(r, "admin.sessions.end", strings.Join(slices.Compact(slices.Sorted(slices.Values(names))), ", "), fmt.Sprintf("%d session(s)", len(drop)))
	w.WriteHeader(http.StatusNoContent)
}

// sessionRecord is how s is stored.
func sessionRecord(key string, s *adminSession) types.AdminSessionRecord {
	groups, _ := json.Marshal(s.roleGroups)

	return types.AdminSessionRecord{
		ID: key, CSRF: s.CSRF, UserID: s.UserID, Email: s.Email, Name: s.Name, Picture: s.Picture,
		RoleEmail: s.roleEmail, RoleGroups: string(groups), Subject: s.subject,
		RemoteAddr: s.remoteAddr, UserAgent: s.userAgent,
		CreatedAt: s.createdAt.UTC(), LastSeenAt: s.lastSeen.UTC(), ExpiresAt: s.ExpiresAt.UTC(),
	}
}

func sessionFromRecord(rec types.AdminSessionRecord) *adminSession {
	var groups []string
	_ = json.Unmarshal([]byte(rec.RoleGroups), &groups)

	return &adminSession{
		CSRF: rec.CSRF, UserID: rec.UserID, Email: rec.Email, Name: rec.Name, Picture: rec.Picture,
		ExpiresAt: rec.ExpiresAt, roleEmail: rec.RoleEmail, roleGroups: groups, subject: rec.Subject,
		remoteAddr: rec.RemoteAddr, userAgent: rec.UserAgent, createdAt: rec.CreatedAt, lastSeen: rec.LastSeenAt,
		persistedSeen: rec.LastSeenAt,
	}
}
