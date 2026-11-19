package types

import (
	"maps"
	"slices"

	"tailscale.com/tailcfg"
)

// SettingDNS holds the DNS settings made in the admin console, as
// [ConsoleDNS]. They replace the matching dns.* settings of the config file;
// dns.base_domain stays in the file because changing it renames every
// device.
const SettingDNS = "dns.console"

// ConsoleDNS is the DNS clients get, as set in the admin console.
type ConsoleDNS struct {
	MagicDNS         bool                `json:"magicDNS"`
	OverrideLocalDNS bool                `json:"overrideLocalDNS"`
	Nameservers      []string            `json:"nameservers"`
	SplitDNS         map[string][]string `json:"splitDNS"`
	SearchDomains    []string            `json:"searchDomains"`
	// Records are ignored while dns.extra_records_path manages them.
	Records []ConsoleDNSRecord `json:"records"`
}

// ConsoleDNSRecord is an extra DNS record as the console stores it.
type ConsoleDNSRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// ConsoleRecords converts client DNS records for the console.
func ConsoleRecords(in []tailcfg.DNSRecord) []ConsoleDNSRecord {
	out := make([]ConsoleDNSRecord, 0, len(in))
	for _, r := range in {
		out = append(out, ConsoleDNSRecord{Name: r.Name, Type: r.Type, Value: r.Value})
	}

	return out
}

func tailcfgRecords(in []ConsoleDNSRecord) []tailcfg.DNSRecord {
	out := make([]tailcfg.DNSRecord, 0, len(in))
	for _, r := range in {
		out = append(out, tailcfg.DNSRecord{Name: r.Name, Type: r.Type, Value: r.Value})
	}

	return out
}

// ConsoleDNSFrom describes d in console terms, e.g. the config file's
// settings before the console changed anything.
func ConsoleDNSFrom(d DNSConfig) ConsoleDNS {
	split := maps.Clone(d.Nameservers.Split)
	if split == nil {
		split = map[string][]string{}
	}

	return ConsoleDNS{
		MagicDNS:         d.MagicDNS,
		OverrideLocalDNS: d.OverrideLocalDNS,
		Nameservers:      append([]string{}, d.Nameservers.Global...),
		SplitDNS:         split,
		SearchDomains:    append([]string{}, d.SearchDomains...),
		Records:          ConsoleRecords(d.ExtraRecords),
	}
}

// ApplyConsoleDNS replaces the DNS config clients get with the file's
// settings overlaid by cd, or the file's alone when cd is nil. Extra records
// a dns.extra_records_path file manages are kept. It does nothing before
// [Config.StartDNS]. Safe for concurrent use with
// [Config.CloneTailcfgDNSConfig]; send change.DNSConfig afterwards.
func (c *Config) ApplyConsoleDNS(cd *ConsoleDNS) {
	if c.dns == nil {
		return
	}

	d := c.DNSConfig
	if cd != nil {
		d.MagicDNS = cd.MagicDNS && d.BaseDomain != ""
		d.OverrideLocalDNS = cd.OverrideLocalDNS
		d.Nameservers = Nameservers{Global: slices.Clone(cd.Nameservers), Split: maps.Clone(cd.SplitDNS)}
		d.SearchDomains = slices.Clone(cd.SearchDomains)

		if d.ExtraRecordsPath == "" {
			d.ExtraRecords = tailcfgRecords(cd.Records)
		}
	}

	next := c.tailcfgDNSFrom(d)

	c.dns.mu.Lock()
	defer c.dns.mu.Unlock()

	if c.DNSConfig.ExtraRecordsPath != "" {
		next.ExtraRecords = c.dns.dns.ExtraRecords
	}

	c.dns.dns = next
}
