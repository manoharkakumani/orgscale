package hscontrol

import (
	"testing"
	"time"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/stretchr/testify/assert"
)

func TestIntuneMatchesOnSerialOnly(t *testing.T) {
	p := &postureChecker{
		intuneAt: time.Now(),
		intune: []intuneDevice{
			{DeviceName: "pat-mac", SerialNumber: "C02ABC", ComplianceState: "compliant", UserPrincipalName: "pat@example.com"},
			{DeviceName: "lee-pc", SerialNumber: "PF3XYZ", ComplianceState: "noncompliant", UserPrincipalName: "lee@example.com"},
			{DeviceName: "shared", SerialNumber: "KIOSK1", ComplianceState: "compliant"},
		},
	}
	claims := map[string]int{"c02abc": 1, "pf3xyz": 1, "kiosk1": 1, "dup1": 2}

	assert.Equal(t, types.IntuneCompliant, p.intuneStateLocked([]string{"c02abc"}, "Pat@example.com", claims))
	assert.Equal(t, types.IntuneNoncompliant, p.intuneStateLocked([]string{"PF3XYZ"}, "lee@example.com", claims))
	assert.Equal(t, types.IntuneCompliant, p.intuneStateLocked([]string{"KIOSK1"}, "anyone@example.com", claims), "a record with no owner matches anyone")
	assert.Equal(t, types.IntuneNoSerial, p.intuneStateLocked(nil, "pat@example.com", claims), "a name is never enough")
	assert.Equal(t, types.IntuneOtherOwner, p.intuneStateLocked([]string{"C02ABC"}, "mallory@example.com", claims))
	assert.Equal(t, types.IntuneDuplicate, p.intuneStateLocked([]string{"DUP1"}, "pat@example.com", claims))
	assert.Equal(t, types.IntuneNotManaged, p.intuneStateLocked([]string{"NOPE"}, "pat@example.com", claims))

	p.intuneAt = time.Now().Add(-2 * intuneStale)
	assert.Equal(t, types.IntuneUnavailable, p.intuneStateLocked([]string{"C02ABC"}, "pat@example.com", claims), "a stale answer is not used")
}
