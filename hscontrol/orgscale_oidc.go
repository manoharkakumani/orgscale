package hscontrol

import (
	"errors"
	"net/http"
	"strings"

	"github.com/manoharkakumani/orgscale/hscontrol/state"
	"github.com/manoharkakumani/orgscale/hscontrol/types"
	"github.com/rs/zerolog/log"
)

// Provider values of users that orgscale creates from a directory, before
// they first log in.
const (
	userProviderSCIM   = "scim"
	userProviderGoogle = "google"
)

var errUserDisabled = newHTTPUserError(
	http.StatusForbidden,
	"user is disabled",
	"Your access has been disabled by an administrator.",
	nil,
)

// effectiveOIDCConfig returns cfg with the login rules from the admin console
// applied: a rule stored there replaces the one from the configuration file.
func effectiveOIDCConfig(st *state.State, cfg *types.OIDCConfig) *types.OIDCConfig {
	c := *cfg

	apply := func(key string, dst *[]string) {
		var v []string

		ok, err := st.GetSettingJSON(key, &v)
		if err != nil {
			log.Warn().Err(err).Str("setting", key).Msg("ignoring unreadable login rule")

			return
		}

		if ok {
			*dst = v
		}
	}

	apply(types.SettingSSOAllowedDomains, &c.AllowedDomains)
	apply(types.SettingSSOAllowedUsers, &c.AllowedUsers)
	apply(types.SettingSSOAllowedGroups, &c.AllowedGroups)

	return &c
}

// provisionedUserForClaim finds the user a directory (SCIM or Google)
// created for the person logging in, matched by verified email, so their
// first login takes over that user instead of creating a second one.
//
// The email must be verified by the identity provider whatever
// oidc.email_verified_required says: taking over a user takes over its
// groups and device access.
func provisionedUserForClaim(st *state.State, claims *types.OIDCClaims, _ bool) *types.User {
	if claims.Email == "" || !bool(claims.EmailVerified) {
		return nil
	}

	users, err := st.ListUsersWithFilter(&types.User{Email: claims.Email})
	if err != nil {
		return nil
	}

	var found *types.User

	for i := range users {
		u := &users[i]
		if u.ProviderIdentifier.Valid {
			continue
		}

		if u.Provider != userProviderSCIM && u.Provider != userProviderGoogle {
			continue
		}

		if !strings.EqualFold(u.Email, claims.Email) {
			continue
		}

		if found != nil {
			return nil // ambiguous: leave it to an admin
		}

		found = u
	}

	return found
}

// afterOIDCLogin enforces the admin console's user state on a login that
// passed the identity provider: a disabled user is refused, and the groups
// claim is recorded as the user's OIDC directory groups.
func (a *AuthProviderOIDC) afterOIDCLogin(user *types.User, claims *types.OIDCClaims) error {
	disabled, err := a.h.state.IsUserDisabled(types.UserID(user.ID))
	if err != nil {
		return err
	}

	if disabled {
		return errUserDisabled
	}

	// No groups claim means the provider does not send groups: leave the
	// memberships alone rather than clearing them.
	if claims.Groups == nil {
		return nil
	}

	changes, err := a.h.state.SyncUserOIDCGroups(types.UserID(user.ID), claims.Groups)
	a.h.Change(changes...)

	if err != nil && !errors.Is(err, errUserDisabled) {
		log.Error().Err(err).Uint("user", user.ID).Msg("recording OIDC groups")
	}

	return nil
}
