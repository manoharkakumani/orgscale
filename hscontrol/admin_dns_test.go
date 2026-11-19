package hscontrol

import (
	"testing"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCleanDNS(t *testing.T) {
	ok, err := cleanDNS(types.ConsoleDNS{
		MagicDNS:      true,
		Nameservers:   []string{" 1.1.1.1", "https://dns.example.com/dns-query", "1.1.1.1"},
		SplitDNS:      map[string][]string{"Corp.Example.com.": {"10.0.0.53"}},
		SearchDomains: []string{"corp.example.com", ""},
		Records:       []types.ConsoleDNSRecord{{Name: "Grafana.ts.example", Value: "fd7a:115c:a1e0::3"}},
	}, "ts.example")
	require.NoError(t, err)
	assert.Equal(t, []string{"1.1.1.1", "https://dns.example.com/dns-query"}, ok.Nameservers)
	assert.Equal(t, []string{"10.0.0.53"}, ok.SplitDNS["corp.example.com"])
	assert.Equal(t, []string{"corp.example.com"}, ok.SearchDomains)
	assert.Equal(t, types.ConsoleDNSRecord{Name: "grafana.ts.example", Type: "AAAA", Value: "fd7a:115c:a1e0::3"}, ok.Records[0])

	for name, in := range map[string]types.ConsoleDNS{
		"plain http DoH":        {Nameservers: []string{"http://dns.example.com"}},
		"hostname server":       {Nameservers: []string{"dns.example.com"}},
		"split without servers": {SplitDNS: map[string][]string{"corp.example.com": {}}},
		"bad split domain":      {SplitDNS: map[string][]string{"corp example": {"10.0.0.1"}}},
		"split over MagicDNS":   {MagicDNS: true, SplitDNS: map[string][]string{"example": {"10.0.0.1"}}},
		"record with hostname":  {Records: []types.ConsoleDNSRecord{{Name: "x.ts.example", Value: "host"}}},
		"record without domain": {Records: []types.ConsoleDNSRecord{{Name: "grafana", Value: "100.64.0.1"}}},
	} {
		_, err := cleanDNS(in, "ts.example")
		require.Error(t, err, name)
	}

	off, err := cleanDNS(types.ConsoleDNS{OverrideLocalDNS: true}, "ts.example")
	require.NoError(t, err)
	assert.False(t, off.OverrideLocalDNS, "nothing to override with")

	_, err = cleanDNS(types.ConsoleDNS{MagicDNS: true}, "")
	assert.Error(t, err, "MagicDNS needs a base domain")
}
