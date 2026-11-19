package hscontrol

import (
	"cmp"
	"fmt"
	"net/http"
	"net/netip"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/manoharkakumani/orgscale/hscontrol/types/change"
)

type adminDNS struct {
	types.ConsoleDNS

	BaseDomain string `json:"baseDomain"`
	// Source is "console" when the console's settings are in effect, else
	// "file".
	Source string `json:"source"`
	// RecordsFromFile is set while dns.extra_records_path manages records.
	RecordsFromFile bool `json:"recordsFromFile"`
}

func (aa *adminAuth) dnsView() (adminDNS, error) {
	cfg := aa.h.cfg
	v := adminDNS{
		ConsoleDNS:      types.ConsoleDNSFrom(cfg.DNSConfig),
		BaseDomain:      cfg.DNSConfig.BaseDomain,
		Source:          "file",
		RecordsFromFile: cfg.DNSConfig.ExtraRecordsPath != "",
	}

	var cd types.ConsoleDNS

	ok, err := aa.h.state.GetSettingJSON(types.SettingDNS, &cd)
	if err != nil {
		return v, err
	}

	if ok {
		v.ConsoleDNS, v.Source = cd, "console"
	}

	v.OverrideLocalDNS = v.OverrideLocalDNS && len(v.Nameservers) > 0

	if v.SplitDNS == nil {
		v.SplitDNS = map[string][]string{}
	}

	for _, l := range []*[]string{&v.Nameservers, &v.SearchDomains} {
		if *l == nil {
			*l = []string{}
		}
	}

	if v.RecordsFromFile {
		v.Records = types.ConsoleRecords(cfg.CloneTailcfgDNSConfig().ExtraRecords)
	}

	if v.Records == nil {
		v.Records = []types.ConsoleDNSRecord{}
	}

	return v, nil
}

func (aa *adminAuth) handleGetDNS(w http.ResponseWriter, _ *http.Request) {
	v, err := aa.dnsView()
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	adminJSON(w, http.StatusOK, v)
}

// validNameserverAddr accepts an IP address or an https DNS-over-HTTPS URL.
func validNameserverAddr(ns string) bool {
	_, err := netip.ParseAddr(ns)
	if err == nil {
		return true
	}

	u, err := url.Parse(ns)

	return err == nil && u.Scheme == "https" && u.Host != ""
}

var dnsDomainRE = regexp.MustCompile(`^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$`)

// validDNSDomain accepts a lowercase domain such as corp.example.com.
func validDNSDomain(d string) bool {
	return len(d) <= 253 && dnsDomainRE.MatchString(d)
}

func cleanDNS(in types.ConsoleDNS, baseDomain string) (types.ConsoleDNS, error) {
	out := types.ConsoleDNS{MagicDNS: in.MagicDNS, OverrideLocalDNS: in.OverrideLocalDNS, SplitDNS: map[string][]string{}}

	if out.MagicDNS && baseDomain == "" {
		return out, userError("MagicDNS needs dns.base_domain in the config file")
	}

	servers := func(list []string, where string) ([]string, error) {
		var res []string

		for _, ns := range list {
			ns = strings.TrimSpace(ns)
			if ns == "" {
				continue
			}

			if !validNameserverAddr(ns) {
				return nil, userErrorf("%s: %q is not an IP address or https:// DNS-over-HTTPS address", where, ns)
			}

			if !slices.Contains(res, ns) {
				res = append(res, ns)
			}
		}

		return res, nil
	}

	var err error

	out.Nameservers, err = servers(in.Nameservers, "DNS servers")
	if err != nil {
		return out, err
	}

	// With no server there is nothing to use instead; clients keep theirs.
	out.OverrideLocalDNS = out.OverrideLocalDNS && len(out.Nameservers) > 0

	for domain, list := range in.SplitDNS {
		domain = strings.ToLower(strings.Trim(strings.TrimSpace(domain), "."))
		if !validDNSDomain(domain) {
			return out, userErrorf("%q is not a domain", domain)
		}

		if baseDomain != "" && (domain == baseDomain || strings.HasSuffix(baseDomain, "."+domain)) && out.MagicDNS {
			return out, userErrorf("%s holds the MagicDNS domain %s; send a subdomain instead", domain, baseDomain)
		}

		ns, err := servers(list, domain)
		if err != nil {
			return out, err
		}

		if len(ns) == 0 {
			return out, userErrorf("add a DNS server for %s", domain)
		}

		out.SplitDNS[domain] = ns
	}

	for _, d := range in.SearchDomains {
		d = strings.ToLower(strings.Trim(strings.TrimSpace(d), "."))
		if d == "" {
			continue
		}

		if !validDNSDomain(d) {
			return out, userErrorf("%q is not a domain", d)
		}

		if !slices.Contains(out.SearchDomains, d) {
			out.SearchDomains = append(out.SearchDomains, d)
		}
	}

	for _, r := range in.Records {
		name := strings.ToLower(strings.Trim(strings.TrimSpace(r.Name), "."))
		if !validDNSDomain(name) || !strings.Contains(name, ".") {
			return out, userErrorf("%q is not a full name, e.g. grafana.%s", r.Name, cmp.Or(baseDomain, "example.com"))
		}

		ip, err := netip.ParseAddr(strings.TrimSpace(r.Value))
		if err != nil {
			return out, userErrorf("%s: %q is not an IP address", name, r.Value)
		}

		typ := "A"
		if ip.Is6() {
			typ = "AAAA"
		}

		out.Records = append(out.Records, types.ConsoleDNSRecord{Name: name, Type: typ, Value: ip.String()})
	}

	if len(out.Records) > 1000 {
		return out, userError("at most 1000 records")
	}

	return out, nil
}

func (aa *adminAuth) applyDNS(r *http.Request, cd *types.ConsoleDNS, action, detail string) {
	aa.h.cfg.ApplyConsoleDNS(cd)
	aa.h.state.WarnNodesHoldingExtraRecords()
	aa.h.Change(change.DNSConfig())
	aa.auditReq(r, action, "dns", detail)
}

func (aa *adminAuth) handlePutDNS(w http.ResponseWriter, r *http.Request) {
	var in types.ConsoleDNS
	if !decodeJSON(w, r, &in) {
		return
	}

	cd, err := cleanDNS(in, aa.h.cfg.DNSConfig.BaseDomain)
	if err != nil {
		adminJSONError(w, http.StatusBadRequest, err.Error())

		return
	}

	if aa.h.cfg.DNSConfig.ExtraRecordsPath != "" {
		cd.Records = nil
	}

	err = aa.h.state.SetSettingJSON(types.SettingDNS, cd, sessionFrom(r).label())
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.applyDNS(r, &cd, "dns.save", fmt.Sprintf("MagicDNS %t, %d server(s), %d split domain(s), %d record(s)",
		cd.MagicDNS, len(cd.Nameservers), len(cd.SplitDNS), len(cd.Records)))

	aa.handleGetDNS(w, r)
}

func (aa *adminAuth) handleResetDNS(w http.ResponseWriter, r *http.Request) {
	err := aa.h.state.DeleteSetting(types.SettingDNS)
	if err != nil {
		adminJSONError(w, http.StatusInternalServerError, err.Error())

		return
	}

	aa.applyDNS(r, nil, "dns.reset", "back to the config file")
	aa.handleGetDNS(w, r)
}
