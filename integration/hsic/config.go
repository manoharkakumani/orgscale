package hsic

import "github.com/manoharkakumani/orgscale/hscontrol/types"

func MinimumConfigYAML() string {
	return `
noise:
  private_key_path: /tmp/noise_private.key
`
}

func DefaultConfigEnv() map[string]string {
	return map[string]string{
		"ORGSCALE_LOG_LEVEL":                         "trace",
		"ORGSCALE_POLICY_PATH":                       "",
		"ORGSCALE_DATABASE_TYPE":                     "sqlite",
		"ORGSCALE_DATABASE_SQLITE_PATH":              "/tmp/integration_test_db.sqlite3",
		"ORGSCALE_DATABASE_DEBUG":                    "0",
		"ORGSCALE_DATABASE_GORM_SLOW_THRESHOLD":      "1",
		"ORGSCALE_NODE_EPHEMERAL_INACTIVITY_TIMEOUT": "30m",
		"ORGSCALE_PREFIXES_V4":                       "100.64.0.0/10",
		"ORGSCALE_PREFIXES_V6":                       "fd7a:115c:a1e0::/48",
		"ORGSCALE_DNS_BASE_DOMAIN":                   "headscale.net",
		"ORGSCALE_DNS_MAGIC_DNS":                     "true",
		"ORGSCALE_DNS_OVERRIDE_LOCAL_DNS":            "false",
		"ORGSCALE_DNS_NAMESERVERS_GLOBAL":            "127.0.0.11 1.1.1.1",
		"ORGSCALE_NOISE_PRIVATE_KEY_PATH":            "/tmp/noise_private.key",
		"ORGSCALE_METRICS_LISTEN_ADDR":               "0.0.0.0:9090",

		// Embedded DERP is the default for test isolation.
		// Tests should not depend on external DERP infrastructure.
		// Use [WithPublicDERP] to opt out for tests that explicitly
		// need public DERP relays.
		"ORGSCALE_DERP_URLS":                    "[]",
		"ORGSCALE_DERP_AUTO_UPDATE_ENABLED":     "false",
		"ORGSCALE_DERP_UPDATE_FREQUENCY":        "1m",
		"ORGSCALE_DERP_SERVER_ENABLED":          "true",
		"ORGSCALE_DERP_SERVER_REGION_ID":        "999",
		"ORGSCALE_DERP_SERVER_REGION_CODE":      binOrgscale,
		"ORGSCALE_DERP_SERVER_REGION_NAME":      "Orgscale Embedded DERP",
		"ORGSCALE_DERP_SERVER_STUN_LISTEN_ADDR": "0.0.0.0:3478",
		"ORGSCALE_DERP_SERVER_PRIVATE_KEY_PATH": "/tmp/derp.key",
		"DERP_DEBUG_LOGS":                        "true",
		"DERP_PROBER_DEBUG_LOGS":                 "true",

		// a bunch of tests (ACL/Policy) rely on predictable IP alloc,
		// so ensure the sequential alloc is used by default.
		"ORGSCALE_PREFIXES_ALLOCATION": string(types.IPAllocationStrategySequential),
	}
}
