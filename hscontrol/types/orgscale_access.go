package types

import "time"

// More principal kinds, used by access rules and server tag access.
const (
	// PrincipalTag is the devices carrying a tag, by name with "tag:".
	PrincipalTag = "tag"
	// PrincipalDevice is one device, by node ID.
	PrincipalDevice = "device"
	// PrincipalEveryone is every user's devices (autogroup:member). Its
	// Principal is empty.
	PrincipalEveryone = "everyone"
	// PrincipalSelf is each person's own devices (autogroup:self), as an SSH
	// destination. Its Principal is empty.
	PrincipalSelf = "self"
)

// Access rule kinds.
const (
	RuleKindNetwork = "network"
	RuleKindSSH     = "ssh"
)

// Sides of an access rule.
const (
	RuleSideSource      = "src"
	RuleSideDestination = "dst"
)

// ServerTag is a tag defined from the admin console. Servers and VMs
// enrolled with it are owned by the tag rather than a person, and the tag's
// access list says who may reach them.
type ServerTag struct {
	Name        string    `gorm:"column:name;primaryKey" json:"name"`
	Description string    `gorm:"column:description"     json:"description"`
	CreatedAt   time.Time `gorm:"column:created_at"      json:"createdAt"`
	CreatedBy   string    `gorm:"column:created_by"      json:"createdBy"`
}

// TableName pins the gorm table name.
func (ServerTag) TableName() string { return "server_tags" }

// ServerTagAccess grants a user or group access to every device carrying a
// tag.
type ServerTagAccess struct {
	Tag           string    `gorm:"column:tag;primaryKey"`
	PrincipalKind string    `gorm:"column:principal_kind;primaryKey"`
	Principal     string    `gorm:"column:principal;primaryKey"`
	CreatedAt     time.Time `gorm:"column:created_at"`
}

// TableName pins the gorm table name.
func (ServerTagAccess) TableName() string { return "server_tag_access" }

// AccessRule lets its sources reach its destinations on Ports over Protocol.
// Rules are built in the console's rule editor; nobody writes policy JSON.
type AccessRule struct {
	ID       uint64 `gorm:"column:id;primaryKey"`
	Kind     string `gorm:"column:kind"` // RuleKindNetwork or RuleKindSSH
	Name     string `gorm:"column:name"`
	Protocol string `gorm:"column:protocol"` // "" (any), "tcp", "udp" or "icmp"
	Ports    string `gorm:"column:ports"`    // "*" or "22,443,8000-8100"
	Position int    `gorm:"column:position"`
	Enabled  bool   `gorm:"column:enabled"`

	// SSHUsers is a JSON array of the Linux accounts an SSH rule allows,
	// e.g. ["ubuntu","autogroup:nonroot"].
	SSHUsers string `gorm:"column:ssh_users"`
	// SSHCheckPeriod, when set (e.g. "12h"), makes an SSH rule ask people to
	// sign in again through single sign-on at that interval.
	SSHCheckPeriod string `gorm:"column:ssh_check_period"`

	CreatedAt time.Time `gorm:"column:created_at"`
	UpdatedAt time.Time `gorm:"column:updated_at"`
	UpdatedBy string    `gorm:"column:updated_by"`

	Sources      []RulePrincipal `gorm:"-"`
	Destinations []RulePrincipal `gorm:"-"`
}

// TableName pins the gorm table name.
func (AccessRule) TableName() string { return "access_rules" }

// RulePrincipal is one source or destination of an access rule.
type RulePrincipal struct {
	RuleID    uint64 `gorm:"column:rule_id;primaryKey"`
	Side      string `gorm:"column:side;primaryKey"`
	Kind      string `gorm:"column:principal_kind;primaryKey"`
	Principal string `gorm:"column:principal;primaryKey"`
}

// TableName pins the gorm table name.
func (RulePrincipal) TableName() string { return "access_rule_principals" }
