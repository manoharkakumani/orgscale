package hscontrol

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"unicode"

	"github.com/manoharkakumani/orgscale/hscontrol/types"
)

// Shared by SCIM and the Google Workspace sync: users are matched by email
// and created before their first login, so groups and device access can
// name them; their first SSO login takes the user over (see
// provisionedUserForClaim).

func contextWithSession(ctx context.Context, s *adminSession) context.Context {
	return context.WithValue(ctx, adminSessionKey{}, s)
}

// directoryUsername turns an email into a valid, unused username.
func (h *Orgscale) directoryUsername(email string) (string, error) {
	var b strings.Builder

	at := false

	for _, r := range strings.ToLower(email) {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '.' || r == '_':
			b.WriteRune(r)
		case r == '@' && !at:
			b.WriteRune(r)

			at = true
		default:
			b.WriteByte('-')
		}
	}

	base := b.String()
	if base == "" || !unicode.IsLetter(rune(base[0])) {
		base = "u" + base
	}

	if len(base) < 2 {
		base += "-user"
	}

	for i := 1; i < 1000; i++ {
		name := base
		if i > 1 {
			name = base + "-" + strconv.Itoa(i)
		}

		users, err := h.state.ListUsersWithFilter(&types.User{Name: name})
		if err != nil {
			return "", err
		}

		if len(users) == 0 {
			return name, nil
		}
	}

	return "", fmt.Errorf("no free username for %q", email)
}

// userByEmail returns the one user with email (case-insensitive), nil when
// there is none, and an error when several share it.
func (h *Orgscale) userByEmail(email string) (*types.User, error) {
	email = strings.TrimSpace(email)
	if email == "" {
		return nil, nil
	}

	users, err := h.state.ListAllUsers()
	if err != nil {
		return nil, err
	}

	var found *types.User

	for i := range users {
		if !strings.EqualFold(users[i].Email, email) {
			continue
		}

		if found != nil {
			return nil, fmt.Errorf("several users have the email %q", email)
		}

		found = &users[i]
	}

	return found, nil
}

var errInvalidDirectoryEmail = errors.New("not a valid email address")

// validDirectoryEmail reports whether email can name a user in policies:
// one '@' with text on both sides, no whitespace or control characters.
func validDirectoryEmail(email string) bool {
	local, domain, ok := strings.Cut(email, "@")
	if !ok || local == "" || domain == "" || strings.Contains(domain, "@") || !strings.Contains(domain, ".") {
		return false
	}

	return !strings.ContainsFunc(email, func(r rune) bool { return unicode.IsSpace(r) || unicode.IsControl(r) || r == ',' || r == '"' })
}

// provisionUser returns the user with email, creating it for provider
// when there is none.
func (h *Orgscale) provisionUser(email, displayName, picture, provider string) (*types.User, bool, error) {
	email = strings.TrimSpace(email)
	if !validDirectoryEmail(email) {
		return nil, false, fmt.Errorf("%w: %q", errInvalidDirectoryEmail, email)
	}

	u, err := h.userByEmail(email)
	if err != nil || u != nil {
		return u, false, err
	}

	name, err := h.directoryUsername(email)
	if err != nil {
		return nil, false, err
	}

	created, c, err := h.state.CreateUser(types.User{
		Name:          name,
		DisplayName:   displayName,
		Email:         strings.ToLower(strings.TrimSpace(email)),
		Provider:      provider,
		ProfilePicURL: picture,
	})
	if err != nil {
		return nil, false, fmt.Errorf("creating user %q: %w", email, err)
	}

	h.Change(c)

	return created, true, nil
}

// offboardUser removes uid from orgscale, as when the directory deletes or
// deactivates them: their devices, personal keys, grants and the user go.
func (h *Orgscale) offboardUser(uid types.UserID, actor, actorKind, reason string) error {
	label := strconv.FormatUint(uint64(uid), 10)
	if u, err := h.state.GetUserByID(uid); err == nil {
		label = u.Email
		if label == "" {
			label = u.Name
		}
	}

	changes, err := h.state.RemoveUser(uid)
	h.Change(changes...)

	if err != nil {
		return err
	}

	if h.admin != nil {
		h.admin.endSessionsOf(label)
	}

	_ = h.state.AppendAuditEvent(types.AuditEvent{
		Actor: actor, ActorKind: actorKind, Action: "user.remove", Target: label, Detail: reason,
	})

	return nil
}
