package types

import "time"

// Setting keys stored in the settings table. Values are JSON. A key that is
// absent falls back to the configuration file.
const (
	// SettingSSOAllowedDomains is []string: email domains allowed to log in.
	SettingSSOAllowedDomains = "sso.allowed_domains"
	// SettingSSOAllowedUsers is []string: email addresses allowed to log in.
	SettingSSOAllowedUsers = "sso.allowed_users"
	// SettingSSOAllowedGroups is []string: IdP groups allowed to log in.
	SettingSSOAllowedGroups = "sso.allowed_groups"
	// SettingAdminUsers is []string: emails given the admin role, in
	// addition to admin.users from the configuration file.
	SettingAdminUsers = "admin.users"
	// SettingAdminGroups is []string: IdP groups given the admin role, in
	// addition to admin.groups from the configuration file.
	SettingAdminGroups = "admin.groups"
	// SettingViewerUsers is []string: emails given the read-only viewer role.
	SettingViewerUsers = "viewer.users"
	// SettingViewerGroups is []string: IdP groups given the viewer role.
	SettingViewerGroups = "viewer.groups"
	// SettingSCIMTokenHash is string: SHA-256 hex of the SCIM bearer token.
	SettingSCIMTokenHash = "scim.token_hash"
	// SettingSCIMTokenCreatedAt is time.Time: when the SCIM token was made.
	SettingSCIMTokenCreatedAt = "scim.token_created_at"
	// SettingGoogleSync is GoogleSyncSettings.
	SettingGoogleSync = "google.sync"
	// SettingGoogleSyncStatus is GoogleSyncStatus.
	SettingGoogleSyncStatus = "google.sync_status"
)

// Setting is one row of the settings table.
type Setting struct {
	Key       string    `gorm:"column:key;primaryKey"`
	Value     string    `gorm:"column:value"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UpdatedBy string    `gorm:"column:updated_by"`
}

// TableName pins the gorm table name.
func (Setting) TableName() string { return "settings" }

// Directory group sources.
const (
	DirectorySourceOIDC   = "oidc"
	DirectorySourceSCIM   = "scim"
	DirectorySourceGoogle = "google"
)

// DirectoryGroup is a group synced from the identity provider, through the
// OIDC groups claim, SCIM, or the Google Workspace Directory API. Name is the
// policy name: the group is usable in ACLs as `group:<Name>`.
type DirectoryGroup struct {
	ID          uint64    `gorm:"column:id;primaryKey"`
	Source      string    `gorm:"column:source"`
	ExternalID  string    `gorm:"column:external_id"`
	Name        string    `gorm:"column:name"`
	DisplayName string    `gorm:"column:display_name"`
	CreatedAt   time.Time `gorm:"column:created_at"`
	UpdatedAt   time.Time `gorm:"column:updated_at"`

	// MemberIDs is filled by the queries that list groups; not a column.
	MemberIDs []UserID `gorm:"-"`
}

// TableName pins the gorm table name.
func (DirectoryGroup) TableName() string { return "directory_groups" }

// DirectoryGroupMember links a directory group to a user.
type DirectoryGroupMember struct {
	GroupID uint64 `gorm:"column:group_id;primaryKey"`
	UserID  UserID `gorm:"column:user_id;primaryKey"`
}

// TableName pins the gorm table name.
func (DirectoryGroupMember) TableName() string { return "directory_group_members" }

// UserAccess is a user's access state. A disabled user cannot log in and
// owns no nodes: disabling deletes them.
type UserAccess struct {
	UserID     UserID     `gorm:"column:user_id;primaryKey"`
	DisabledAt *time.Time `gorm:"column:disabled_at"`
	DisabledBy string     `gorm:"column:disabled_by"`
}

// TableName pins the gorm table name.
func (UserAccess) TableName() string { return "user_access" }

// Audit actor kinds.
const (
	AuditActorAdmin  = "admin"
	AuditActorAPIKey = "api_key"
	AuditActorSCIM   = "scim"
	AuditActorSystem = "system"
	AuditActorUser   = "user"
)

// AuditEvent is one entry of the audit log.
type AuditEvent struct {
	ID        uint64    `gorm:"column:id;primaryKey"       json:"id"`
	CreatedAt time.Time `gorm:"column:created_at"          json:"createdAt"`
	Actor     string    `gorm:"column:actor"               json:"actor"`
	ActorKind string    `gorm:"column:actor_kind"          json:"actorKind"`
	Action    string    `gorm:"column:action"              json:"action"`
	Target    string    `gorm:"column:target"              json:"target"`
	Detail    string    `gorm:"column:detail"              json:"detail"`
}

// TableName pins the gorm table name.
func (AuditEvent) TableName() string { return "audit_events" }

// GoogleSyncSettings configure the Google Workspace directory sync. The
// service account needs domain-wide delegation with the read-only directory
// scopes, and impersonates AdminEmail.
type GoogleSyncSettings struct {
	Enabled            bool   `json:"enabled"`
	AdminEmail         string `json:"adminEmail"`
	Customer           string `json:"customer"`
	ServiceAccountJSON string `json:"serviceAccountJson,omitempty"`
	IntervalMinutes    int    `json:"intervalMinutes"`
}

// GoogleSyncStatus is the outcome of the last Google directory sync.
type GoogleSyncStatus struct {
	LastRun  time.Time `json:"lastRun"`
	OK       bool      `json:"ok"`
	Error    string    `json:"error,omitempty"`
	Users    int       `json:"users"`
	Groups   int       `json:"groups"`
	Disabled int       `json:"disabled"`
	Created  int       `json:"created"`
}

// Device access principal kinds.
const (
	// PrincipalUser is a user, by user ID.
	PrincipalUser = "user"
	// PrincipalDirectoryGroup is a directory group, by its ID.
	PrincipalDirectoryGroup = "directory_group"
	// PrincipalPolicyGroup is a group defined in the policy file, by name
	// without the "group:" prefix.
	PrincipalPolicyGroup = "policy_group"
)

// SettingDeviceAccessEnabled is bool: when true, users only see their own
// devices and the devices assigned to them, directly or through a group.
const SettingDeviceAccessEnabled = "access.device_assignments"

// DeviceAccess grants a user or group access to a device. With device access
// restriction on, a device is visible only to its owner and its grantees.
type DeviceAccess struct {
	NodeID        NodeID    `gorm:"column:node_id;primaryKey"        json:"nodeId"`
	PrincipalKind string    `gorm:"column:principal_kind;primaryKey" json:"principalKind"`
	Principal     string    `gorm:"column:principal;primaryKey"      json:"principal"`
	CreatedAt     time.Time `gorm:"column:created_at"                json:"createdAt"`
}

// TableName pins the gorm table name.
func (DeviceAccess) TableName() string { return "device_access" }
