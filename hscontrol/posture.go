package hscontrol

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	apiv2 "github.com/manoharkakumani/orgscale/hscontrol/api/v2"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/types/change"
	"github.com/manoharkakumani/orgscale/hscontrol/util/zlog/zf"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

// Device posture: person-owned devices are checked against the requirements
// set in the console (operating system, versions, Intune compliance). In
// enforce mode a failing device is cut off in the policy (see
// state.orgscaleGate) until it passes again; in monitor mode it is only
// reported. Tagged devices, i.e. servers, are never checked.

const (
	postureInterval = 30 * time.Second
	// intuneInterval is how often Intune is asked for compliance.
	intuneInterval = 10 * time.Minute
	// intuneStale is how long a known Intune answer is used while Intune
	// cannot be reached, before every device counts as unknown.
	intuneStale = 24 * time.Hour
	// serialInterval is how often a connected device is asked for its serial
	// numbers again.
	serialInterval = 6 * time.Hour
)

// auditSystemActor names orgscale itself in the audit log.
const auditSystemActor = "orgscale"

// intuneDevice is a managed device as Microsoft Graph lists it.
type intuneDevice struct {
	DeviceName        string `json:"deviceName"`
	SerialNumber      string `json:"serialNumber"`
	ComplianceState   string `json:"complianceState"`
	UserPrincipalName string `json:"userPrincipalName"`
	EmailAddress      string `json:"emailAddress"`
}

type serialEntry struct {
	serials []string
	at      time.Time
}

// devicePosture is one device's last check.
type devicePosture struct {
	ID               uint64   `json:"id"`
	Name             string   `json:"name"`
	User             string   `json:"user"`
	OS               string   `json:"os"`
	OSLabel          string   `json:"osLabel"`
	OSVersion        string   `json:"osVersion"`
	TailscaleVersion string   `json:"tailscaleVersion"`
	Intune           string   `json:"intune,omitempty"`
	SerialKnown      bool     `json:"serialKnown"`
	Online           bool     `json:"online"`
	Passing          bool     `json:"passing"`
	Blocked          bool     `json:"blocked"`
	Reasons          []string `json:"reasons"`
}

type postureChecker struct {
	h    *Orgscale
	kick chan struct{}

	// checkMu makes checks run one at a time, so an older result never
	// replaces a newer one.
	checkMu sync.Mutex

	mu        sync.Mutex
	serials   map[types.NodeID]serialEntry
	intune    []intuneDevice
	intuneAt  time.Time
	intuneTry time.Time
	intuneErr string
	devices   []devicePosture
	checkedAt time.Time
}

func newPostureChecker(h *Orgscale) *postureChecker {
	return &postureChecker{h: h, kick: make(chan struct{}, 1), serials: map[types.NodeID]serialEntry{}}
}

// Kick runs a check soon.
func (p *postureChecker) Kick() {
	select {
	case p.kick <- struct{}{}:
	default:
	}
}

func (p *postureChecker) rules() (types.PostureRules, error) {
	var r types.PostureRules

	_, err := p.h.state.GetSettingJSON(types.SettingPosture, &r)

	return r, err
}

// Prime cuts off every person-owned device when posture is enforced, until
// the first check lets the passing ones back in. Call it before devices
// connect.
func (p *postureChecker) Prime() {
	r, err := p.rules()
	if err != nil {
		// Unknown requirements: block rather than let everyone in.
		log.Error().Err(err).Msg("device posture: unreadable requirements; person-owned devices stay cut off until they can be read")
		p.h.state.SetPosture(true, nil)

		return
	}

	if r.Mode == types.PostureEnforce {
		p.h.state.SetPosture(true, nil)
	}
}

// Run checks devices until ctx ends.
func (p *postureChecker) Run(ctx context.Context) {
	tick := time.NewTicker(postureInterval)
	defer tick.Stop()

	for {
		p.Check(ctx, false)

		select {
		case <-ctx.Done():
			return
		case <-tick.C:
		case <-p.kick:
		}
	}
}

// Check evaluates every person-owned device and applies the result;
// refreshIntune asks Intune again even when its answer is recent.
func (p *postureChecker) Check(ctx context.Context, refreshIntune bool) {
	p.checkMu.Lock()
	defer p.checkMu.Unlock()

	rules, err := p.rules()
	if err != nil {
		// Keep whatever is in force rather than lifting it.
		log.Warn().Err(err).Msg("device posture: unreadable requirements; keeping the current decisions")

		return
	}

	if !rules.Active() {
		p.mu.Lock()
		p.devices, p.checkedAt = nil, time.Now()
		p.mu.Unlock()

		p.apply(false, nil, nil)

		return
	}

	if rules.RequireIntune {
		p.collectSerials(ctx)
		p.refreshIntune(ctx, refreshIntune)
	}

	p.mu.Lock()

	nodes := p.h.state.ListNodes()

	// A serial number two devices report proves neither.
	claims := map[string]int{}

	for _, n := range nodes.All() {
		if n.IsTagged() {
			continue
		}

		for _, sn := range p.serials[n.ID()].serials {
			claims[strings.ToLower(sn)]++
		}
	}

	var devices []devicePosture

	passed := map[types.NodeID]struct{}{}

	for _, n := range nodes.All() {
		if n.IsTagged() {
			continue
		}

		hi := n.Hostinfo()
		facts := types.PostureFacts{}

		if hi.Valid() {
			facts.OS, facts.OSVersion, facts.TailscaleVersion = hi.OS(), hi.OSVersion(), hi.IPNVersion()
		}

		owner := n.User()
		email := ""

		if owner.Valid() {
			email = owner.Email()
		}

		if rules.RequireIntune {
			facts.Intune = p.intuneStateLocked(p.serials[n.ID()].serials, email, claims)
		}

		reasons := rules.Check(facts)
		d := devicePosture{
			ID: n.ID().Uint64(), Name: n.GivenName(), User: cmpOr(email, owner.Username()),
			OS: facts.OS, OSLabel: types.PostureOSLabel(facts.OS), OSVersion: facts.OSVersion,
			TailscaleVersion: strings.SplitN(facts.TailscaleVersion, "-", 2)[0],
			Intune:           facts.Intune, SerialKnown: len(p.serials[n.ID()].serials) > 0,
			Online:  n.IsOnline().Valid() && n.IsOnline().Get(),
			Passing: len(reasons) == 0, Reasons: reasons,
		}

		if d.Reasons == nil {
			d.Reasons = []string{}
		}

		if d.Passing {
			passed[n.ID()] = struct{}{}
		} else if rules.Mode == types.PostureEnforce {
			d.Blocked = true
		}

		devices = append(devices, d)
	}

	slices.SortFunc(devices, func(a, b devicePosture) int {
		if a.Passing != b.Passing {
			if a.Passing {
				return 1
			}

			return -1
		}

		return strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name))
	})

	p.devices, p.checkedAt = devices, time.Now()
	p.mu.Unlock()

	p.apply(rules.Mode == types.PostureEnforce, passed, devices)
}

func cmpOr(a, b string) string {
	if a != "" {
		return a
	}

	return b
}

// apply puts a check into force, recording each device cut off or let back
// in.
func (p *postureChecker) apply(enforce bool, passed map[types.NodeID]struct{}, devices []devicePosture) {
	nodes := p.h.state.ListNodes()
	before := map[types.NodeID]bool{}

	for _, n := range nodes.All() {
		before[n.ID()] = p.h.state.PostureBlocked(n.ID())
	}

	c := p.h.state.SetPosture(enforce, passed)
	if c.IsEmpty() {
		return
	}

	p.h.Change(c)

	if p.h.admin == nil {
		return
	}

	reasons := make(map[types.NodeID][]string, len(devices))
	for _, d := range devices {
		reasons[types.NodeID(d.ID)] = d.Reasons
	}

	for _, n := range nodes.All() {
		was, now := before[n.ID()], p.h.state.PostureBlocked(n.ID())

		switch {
		case now && !was:
			p.h.admin.audit(types.AuditEvent{
				Actor: auditSystemActor, ActorKind: types.AuditActorSystem, Action: "posture.block",
				Target: n.GivenName(), Detail: strings.Join(reasons[n.ID()], "; "),
			})
		case was && !now:
			p.h.admin.audit(types.AuditEvent{
				Actor: auditSystemActor, ActorKind: types.AuditActorSystem, Action: "posture.unblock", Target: n.GivenName(),
			})
		}
	}
}

// intuneStateLocked is what Intune says about a device with serials owned
// by email. Only the serial number identifies it: names are chosen by
// whoever owns the device. The Intune record must belong to the same person
// when it names one, and a serial claimed by two devices proves nothing.
func (p *postureChecker) intuneStateLocked(serials []string, email string, claims map[string]int) string {
	if p.intuneAt.IsZero() || time.Since(p.intuneAt) > intuneStale {
		return types.IntuneUnavailable
	}

	if len(serials) == 0 {
		return types.IntuneNoSerial
	}

	for _, sn := range serials {
		if claims[strings.ToLower(sn)] > 1 {
			return types.IntuneDuplicate
		}
	}

	var match *intuneDevice

	for i := range p.intune {
		d := &p.intune[i]
		if d.SerialNumber != "" && slices.ContainsFunc(serials, func(s string) bool { return strings.EqualFold(s, d.SerialNumber) }) {
			match = d

			break
		}
	}

	switch {
	case match == nil:
		return types.IntuneNotManaged
	case (match.UserPrincipalName != "" || match.EmailAddress != "") &&
		!strings.EqualFold(match.UserPrincipalName, email) && !strings.EqualFold(match.EmailAddress, email):
		return types.IntuneOtherOwner
	case match.ComplianceState == "compliant" || match.ComplianceState == "inGracePeriod":
		return types.IntuneCompliant
	}

	return types.IntuneNoncompliant
}

// collectSerials asks connected devices that have not said lately for
// their serial numbers, a few at a time.
func (p *postureChecker) collectSerials(ctx context.Context) {
	b := apiv2.Backend{State: p.h.state, Change: p.h.Change, Cfg: p.h.cfg, Connected: p.h.isConnected}

	var due []types.NodeID

	p.mu.Lock()

	for _, n := range p.h.state.ListNodes().All() {
		if n.IsTagged() || !p.h.isConnected(n.ID()) {
			continue
		}

		if e, ok := p.serials[n.ID()]; ok && time.Since(e.at) < serialInterval {
			continue
		}

		due = append(due, n.ID())
	}

	p.mu.Unlock()

	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(4)

	for _, id := range due {
		g.Go(func() error {
			serials, err := apiv2.PostureIdentity(gctx, b, id)
			if err != nil {
				log.Debug().Err(err).Uint64(zf.NodeID, id.Uint64()).Msg("device posture: no serial numbers")
			}

			p.mu.Lock()
			p.serials[id] = serialEntry{serials: serials, at: time.Now()}
			p.mu.Unlock()

			return nil
		})
	}

	_ = g.Wait()
}

// refreshIntune reloads Intune's managed devices when due.
func (p *postureChecker) refreshIntune(ctx context.Context, force bool) {
	p.mu.Lock()
	due := force || time.Since(p.intuneTry) >= intuneInterval
	p.mu.Unlock()

	if !due {
		return
	}

	devices, err := p.fetchIntune(ctx)

	p.mu.Lock()
	defer p.mu.Unlock()

	p.intuneTry = time.Now()

	if err != nil {
		p.intuneErr = err.Error()
		log.Warn().Err(err).Msg("device posture: reading Intune")

		return
	}

	p.intune, p.intuneAt, p.intuneErr = devices, time.Now(), ""
}

var errIntuneNeedsEntra = userError("Intune compliance needs Microsoft Entra ID: set up Entra directory sync, or single sign-on with Entra ID, first")

func (p *postureChecker) fetchIntune(ctx context.Context) ([]intuneDevice, error) {
	e := p.h.entraSync
	if e == nil {
		return nil, errIntuneNeedsEntra
	}

	creds, err := e.credentials(e.settings())
	if err != nil {
		return nil, errIntuneNeedsEntra
	}

	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()

	devices, _, err := graphList[intuneDevice](ctx, e.graphClient(ctx, creds),
		"/deviceManagement/managedDevices?$select=deviceName,serialNumber,complianceState,userPrincipalName,emailAddress&$top=999")
	if err != nil {
		msg := strings.ToLower(err.Error())
		if strings.Contains(msg, "403") || strings.Contains(msg, "forbidden") || strings.Contains(msg, "authorization") {
			return nil, userError("Microsoft Graph refused to list Intune devices: grant the app registration the application permission DeviceManagementManagedDevices.Read.All, with admin consent")
		}

		return nil, err
	}

	return devices, nil
}

// view is the state of the last check, for the console.
func (p *postureChecker) view() ([]devicePosture, time.Time, string, time.Time, int) {
	p.mu.Lock()
	defer p.mu.Unlock()

	return slices.Clone(p.devices), p.checkedAt, p.intuneErr, p.intuneAt, len(p.intune)
}

// --- console API ---

func (aa *adminAuth) postureRoutes(r chi.Router) {
	r.Get("/posture", aa.handleGetPosture)
	r.Put("/posture", aa.handlePutPosture)
	r.Post("/posture/check", aa.handleCheckPosture)
	r.Get("/recording", aa.handleGetRecording)
	r.Put("/recording", aa.handlePutRecording)
	r.Get("/tailnets", aa.handleGetTailnets)
	r.Put("/tailnets", aa.handlePutTailnets)
}

type adminPosture struct {
	Rules         types.PostureRules `json:"rules"`
	AccessControl bool               `json:"accessControl"`
	EntraReady    bool               `json:"entraReady"`
	IntuneError   string             `json:"intuneError,omitempty"`
	IntuneAt      *time.Time         `json:"intuneAt,omitempty"`
	IntuneDevices int                `json:"intuneDevices"`
	CheckedAt     *time.Time         `json:"checkedAt,omitempty"`
	Devices       []devicePosture    `json:"devices"`
}

func (aa *adminAuth) postureView() (adminPosture, error) {
	p := aa.h.posture

	rules, err := p.rules()
	if err != nil {
		return adminPosture{}, err
	}

	if rules.Mode == "" {
		rules.Mode = types.PostureOff
	}

	if rules.OS == nil {
		rules.OS = map[string]types.OSRequirement{}
	}

	devices, checked, intuneErr, intuneAt, n := p.view()
	if devices == nil {
		devices = []devicePosture{}
	}

	v := adminPosture{
		Rules: rules, AccessControl: aa.h.state.DeviceRestrictionEnabled(),
		IntuneError: intuneErr, IntuneDevices: n, Devices: devices,
	}

	if e := aa.h.entraSync; e != nil {
		_, err := e.credentials(e.settings())
		v.EntraReady = err == nil
	}

	if !intuneAt.IsZero() {
		v.IntuneAt = &intuneAt
	}

	if !checked.IsZero() {
		v.CheckedAt = &checked
	}

	return v, nil
}

func (aa *adminAuth) writePosture(w http.ResponseWriter) {
	v, err := aa.postureView()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	adminJSON(w, http.StatusOK, v)
}

func (aa *adminAuth) handleGetPosture(w http.ResponseWriter, _ *http.Request) {
	aa.writePosture(w)
}

func cleanPostureRules(in types.PostureRules) (types.PostureRules, error) {
	out := types.PostureRules{Mode: in.Mode, RequireIntune: in.RequireIntune, OS: map[string]types.OSRequirement{}}

	switch out.Mode {
	case types.PostureOff, types.PostureMonitor, types.PostureEnforce:
	case "":
		out.Mode = types.PostureOff
	default:
		return out, userErrorf("unknown mode %q", in.Mode)
	}

	out.MinTailscaleVersion = strings.TrimSpace(in.MinTailscaleVersion)
	if out.MinTailscaleVersion != "" && !types.ValidVersion(out.MinTailscaleVersion) {
		return out, userErrorf("%q is not a version such as 1.70", out.MinTailscaleVersion)
	}

	for os, req := range in.OS {
		if !slices.Contains(types.PostureOSes, os) {
			return out, userErrorf("unknown operating system %q", os)
		}

		req.MinVersion = strings.TrimSpace(req.MinVersion)
		if os == "linux" {
			req.MinVersion = ""
		}

		if req.MinVersion != "" && !types.ValidVersion(req.MinVersion) {
			return out, userErrorf("%s: %q is not a version such as 14 or 10.0.22000", types.PostureOSLabel(os), req.MinVersion)
		}

		if req.Blocked || req.MinVersion != "" {
			out.OS[os] = req
		}
	}

	if len(out.OS) == len(types.PostureOSes) && !slices.ContainsFunc(types.PostureOSes, func(os string) bool { return !out.OS[os].Blocked }) {
		return out, userError("every operating system is blocked; no device could connect")
	}

	return out, nil
}

func (aa *adminAuth) handlePutPosture(w http.ResponseWriter, r *http.Request) {
	var in types.PostureRules
	if !decodeJSON(w, r, &in) {
		return
	}

	rules, err := cleanPostureRules(in)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	if rules.RequireIntune {
		e := aa.h.entraSync
		if e == nil {
			adminJSONError(w, http.StatusBadRequest, errIntuneNeedsEntra.Error())

			return
		}

		_, err := e.credentials(e.settings())
		if err != nil {
			adminJSONError(w, http.StatusBadRequest, errIntuneNeedsEntra.Error())

			return
		}
	}

	err = aa.h.state.SetSettingJSON(types.SettingPosture, rules, sessionFrom(r).label())
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "posture.save", "device posture", postureSummary(rules))
	aa.h.posture.Check(r.Context(), rules.RequireIntune)
	aa.writePosture(w)
}

func postureSummary(r types.PostureRules) string {
	parts := []string{"mode " + string(r.Mode)}

	if r.MinTailscaleVersion != "" {
		parts = append(parts, "Tailscale ≥ "+r.MinTailscaleVersion)
	}

	for _, os := range types.PostureOSes {
		req, ok := r.OS[os]
		if !ok {
			continue
		}

		if req.Blocked {
			parts = append(parts, types.PostureOSLabel(os)+" blocked")
		} else {
			parts = append(parts, types.PostureOSLabel(os)+" ≥ "+req.MinVersion)
		}
	}

	if r.RequireIntune {
		parts = append(parts, "Intune compliant")
	}

	return strings.Join(parts, ", ")
}

func (aa *adminAuth) handleCheckPosture(w http.ResponseWriter, r *http.Request) {
	aa.h.posture.Check(r.Context(), true)
	aa.writePosture(w)
}

// --- SSH session recording ---

type adminRecorder struct {
	ID      uint64 `json:"id"`
	Name    string `json:"name"`
	Address string `json:"address"`
	Online  bool   `json:"online"`
}

type adminRecording struct {
	types.RecordingSettings

	AccessControl bool            `json:"accessControl"`
	Tag           string          `json:"tag"`
	Recorders     []adminRecorder `json:"recorders"`
}

func (aa *adminAuth) recordingView() adminRecording {
	v := adminRecording{
		RecordingSettings: aa.h.state.Recording(),
		AccessControl:     aa.h.state.DeviceRestrictionEnabled(),
		Tag:               types.RecorderTag,
		Recorders:         []adminRecorder{},
	}

	for _, n := range aa.h.state.ListNodes().All() {
		if !n.IsTagged() || !slices.Contains(n.Tags().AsSlice(), types.RecorderTag) {
			continue
		}

		rec := adminRecorder{ID: n.ID().Uint64(), Name: n.GivenName(), Online: n.IsOnline().Valid() && n.IsOnline().Get()}
		if ip := n.IPv4(); ip.Valid() {
			rec.Address = ip.String()
		}

		v.Recorders = append(v.Recorders, rec)
	}

	return v
}

func (aa *adminAuth) handleGetRecording(w http.ResponseWriter, _ *http.Request) {
	adminJSON(w, http.StatusOK, aa.recordingView())
}

func (aa *adminAuth) handlePutRecording(w http.ResponseWriter, r *http.Request) {
	var in types.RecordingSettings
	if !decodeJSON(w, r, &in) {
		return
	}

	if !in.Enabled {
		in.Enforce = false
	}

	prev := aa.h.state.Recording()

	err := aa.h.state.SetSettingJSON(types.SettingRecording, in, sessionFrom(r).label())
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.h.state.SetRecording(in)

	// The recorder rule is part of the policy.
	changes, err := aa.h.state.ReloadPolicy()
	if err != nil {
		aa.h.state.SetRecording(prev)
		_ = aa.h.state.SetSettingJSON(types.SettingRecording, prev, sessionFrom(r).label())

		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	aa.h.Change(changes...)
	aa.h.Change(change.PolicyChange())

	detail := "off"
	if in.Enabled {
		detail = "on, sessions go ahead unrecorded if no recorder answers"
		if in.Enforce {
			detail = "on, sessions refused unless recorded"
		}
	}

	aa.auditReq(r, "recording.save", "SSH session recording", detail)
	adminJSON(w, http.StatusOK, aa.recordingView())
}

// --- other tailnets ---

func (aa *adminAuth) handleGetTailnets(w http.ResponseWriter, _ *http.Request) {
	var links []types.TailnetLink

	_, err := aa.h.state.GetSettingJSON(types.SettingTailnets, &links)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	if links == nil {
		links = []types.TailnetLink{}
	}

	adminJSON(w, http.StatusOK, links)
}

func (aa *adminAuth) handlePutTailnets(w http.ResponseWriter, r *http.Request) {
	var in []types.TailnetLink
	if !decodeJSON(w, r, &in) {
		return
	}

	out := []types.TailnetLink{}

	for _, l := range in {
		l.Name, l.URL = strings.TrimSpace(l.Name), strings.TrimRight(strings.TrimSpace(l.URL), "/")
		if l.Name == "" && l.URL == "" {
			continue
		}

		if l.Name == "" || len(l.Name) > 60 {
			adminJSONError(w, http.StatusBadRequest, "give every tailnet a name of up to 60 characters")

			return
		}

		if !validServerURL(l.URL) {
			adminJSONError(w, http.StatusBadRequest, fmt.Sprintf("%s: %q is not an https:// address", l.Name, l.URL))

			return
		}

		out = append(out, l)
	}

	if len(out) > 20 {
		adminJSONError(w, http.StatusBadRequest, "at most 20 tailnets")

		return
	}

	err := aa.h.state.SetSettingJSON(types.SettingTailnets, out, sessionFrom(r).label())
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.auditReq(r, "tailnets.save", "other tailnets", fmt.Sprintf("%d linked", len(out)))
	adminJSON(w, http.StatusOK, out)
}

// validServerURL accepts an https:// address with a host, and http:// for
// localhost.
func validServerURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.User != nil {
		return false
	}

	return u.Scheme == "https" || (u.Scheme == "http" && (u.Hostname() == "localhost" || u.Hostname() == "127.0.0.1"))
}
