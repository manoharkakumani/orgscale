-- This file is the representation of the SQLite schema of Orgscale.
-- It is the "source of truth" and is used to validate any migrations
-- that are run against the database to ensure it ends in the expected state.

CREATE TABLE migrations(id text,PRIMARY KEY(id));

CREATE TABLE users(
  id integer PRIMARY KEY AUTOINCREMENT,
  name text,
  display_name text,
  email text,
  provider_identifier text,
  provider text,
  profile_pic_url text,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime
);
CREATE INDEX idx_users_deleted_at ON users(deleted_at);


-- The following three UNIQUE indexes work together to enforce the user identity model:
--
-- 1. Users can be either local (provider_identifier is NULL) or from external providers (provider_identifier set)
-- 2. Each external provider identifier must be unique across the system
-- 3. Local usernames must be unique among local users
-- 4. The same username can exist across different providers with different identifiers
--
-- Examples:
-- - Can create local user "alice" (provider_identifier=NULL)
-- - Can create external user "alice" with GitHub (name="alice", provider_identifier="alice_github")
-- - Can create external user "alice" with Google (name="alice", provider_identifier="alice_google")
-- - Cannot create another local user "alice" (blocked by idx_name_no_provider_identifier)
-- - Cannot create another user with provider_identifier="alice_github" (blocked by idx_provider_identifier)
-- - Cannot create user "bob" with provider_identifier="alice_github" (blocked by idx_name_provider_identifier)
CREATE UNIQUE INDEX idx_provider_identifier ON users(provider_identifier) WHERE provider_identifier IS NOT NULL;
CREATE UNIQUE INDEX idx_name_provider_identifier ON users(name, provider_identifier);
CREATE UNIQUE INDEX idx_name_no_provider_identifier ON users(name) WHERE provider_identifier IS NULL;

-- Unified store for every authenticatable secret (API keys, pre-auth keys,
-- OAuth clients and access tokens), discriminated by kind. Only a hash of the
-- secret is stored (SHA-256; legacy bcrypt/Argon2id until next use); identifier
-- is the public lookup value, unique within a kind. Per-kind columns are sparse
-- by design.
CREATE TABLE credentials(
  id integer PRIMARY KEY AUTOINCREMENT,
  kind text NOT NULL CHECK(kind IN ('api','authkey','oauth_client','oauth_token')),
  identifier text,
  hash blob,
  user_id integer,
  description text,
  scopes text,
  tags text,
  reusable numeric,
  ephemeral numeric DEFAULT false,
  used numeric DEFAULT false,
  last_seen datetime,
  client_id text,
  created_at datetime,
  expiration datetime,
  revoked datetime,
  created_by_token numeric DEFAULT false,

  CONSTRAINT fk_credentials_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE SET NULL,
  CONSTRAINT chk_credentials_hash CHECK(hash IS NOT NULL OR revoked IS NOT NULL),
  CONSTRAINT chk_credentials_hash_format CHECK(hash IS NULL OR substr(CAST(hash AS TEXT), 1, 1) = '$')
);
CREATE UNIQUE INDEX idx_credentials_identifier ON credentials(kind, identifier);
CREATE INDEX idx_credentials_user_id ON credentials(user_id);

CREATE TABLE nodes(
  id integer PRIMARY KEY AUTOINCREMENT,
  machine_key text,
  node_key text,
  disco_key text,

  endpoints text,
  host_info text,
  ipv4 text,
  ipv6 text,
  last_control_address text,
  hostname text,
  given_name varchar(63),
  -- user_id is NULL for tagged nodes (owned by tags, not a user).
  -- Only set for user-owned nodes (no tags).
  user_id integer,
  register_method text,
  tags text,
  auth_key_id integer,
  ephemeral numeric DEFAULT false,
  last_seen datetime,
  expiry datetime,
  approved_routes text,
  auto_approved_routes text,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime,

  CONSTRAINT fk_nodes_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE,
  CONSTRAINT fk_nodes_auth_key FOREIGN KEY(auth_key_id) REFERENCES credentials(id)
);
CREATE INDEX idx_nodes_auth_key_id ON nodes(auth_key_id);

CREATE TABLE policies(
  id integer PRIMARY KEY AUTOINCREMENT,
  data text,

  created_at datetime,
  updated_at datetime,
  deleted_at datetime
);
CREATE INDEX idx_policies_deleted_at ON policies(deleted_at);

CREATE TABLE database_versions(
  id integer PRIMARY KEY,
  version text NOT NULL,
  updated_at datetime
);

-- orgscale admin console tables (see hscontrol/db/orgscale_admin.go).
CREATE TABLE settings(
  key text PRIMARY KEY,
  value text NOT NULL,
  updated_at datetime,
  updated_by text
);
CREATE TABLE directory_groups(
  id integer PRIMARY KEY AUTOINCREMENT,
  source text NOT NULL,
  external_id text NOT NULL,
  name text NOT NULL,
  display_name text,
  created_at datetime,
  updated_at datetime
);
CREATE UNIQUE INDEX idx_directory_groups_source_external_id ON directory_groups(source, external_id);
CREATE TABLE directory_group_members(
  group_id integer NOT NULL,
  user_id integer NOT NULL,
  PRIMARY KEY(group_id, user_id),
  CONSTRAINT fk_directory_group_members_group FOREIGN KEY(group_id) REFERENCES directory_groups(id) ON DELETE CASCADE,
  CONSTRAINT fk_directory_group_members_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE INDEX idx_directory_group_members_user_id ON directory_group_members(user_id);
CREATE TABLE user_access(
  user_id integer PRIMARY KEY,
  disabled_at datetime,
  disabled_by text,
  CONSTRAINT fk_user_access_user FOREIGN KEY(user_id) REFERENCES users(id) ON DELETE CASCADE
);
CREATE TABLE audit_events(
  id integer PRIMARY KEY AUTOINCREMENT,
  created_at datetime NOT NULL,
  actor text NOT NULL,
  actor_kind text NOT NULL,
  action text NOT NULL,
  target text,
  detail text
);
CREATE INDEX idx_audit_events_created_at ON audit_events(created_at);
CREATE TABLE device_access(
  node_id integer NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  created_at datetime,
  PRIMARY KEY(node_id, principal_kind, principal),
  CONSTRAINT fk_device_access_node FOREIGN KEY(node_id) REFERENCES nodes(id) ON DELETE CASCADE
);

-- orgscale server tags and access rules (see hscontrol/db/orgscale_access.go).
CREATE TABLE server_tags(
  name text PRIMARY KEY,
  description text,
  created_at datetime,
  created_by text,
  request_max_minutes integer NOT NULL DEFAULT 0,
  request_ssh_users text,
  request_principals text
);
CREATE TABLE server_tag_access(
  tag text NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  created_at datetime,
  PRIMARY KEY(tag, principal_kind, principal),
  CONSTRAINT fk_server_tag_access_tag FOREIGN KEY(tag) REFERENCES server_tags(name) ON DELETE CASCADE
);
CREATE TABLE access_rules(
  id integer PRIMARY KEY AUTOINCREMENT,
  name text NOT NULL,
  protocol text NOT NULL DEFAULT '',
  ports text NOT NULL DEFAULT '*',
  position integer NOT NULL DEFAULT 0,
  enabled numeric NOT NULL DEFAULT true,
  kind text NOT NULL DEFAULT 'network',
  ssh_users text,
  ssh_check_period text,
  created_at datetime,
  updated_at datetime,
  updated_by text
);
CREATE TABLE access_rule_principals(
  rule_id integer NOT NULL,
  side text NOT NULL,
  principal_kind text NOT NULL,
  principal text NOT NULL,
  PRIMARY KEY(rule_id, side, principal_kind, principal),
  CONSTRAINT fk_access_rule_principals_rule FOREIGN KEY(rule_id) REFERENCES access_rules(id) ON DELETE CASCADE
);

-- orgscale admin console sessions (see hscontrol/db/orgscale_access.go).
CREATE TABLE admin_sessions(
  id text PRIMARY KEY,
  csrf text NOT NULL,
  user_id integer,
  email text,
  name text,
  picture text,
  role_email text,
  role_groups text,
  subject text,
  remote_addr text,
  user_agent text,
  created_at datetime,
  last_seen_at datetime,
  expires_at datetime NOT NULL
);
CREATE INDEX idx_admin_sessions_expires_at ON admin_sessions(expires_at);

-- orgscale just-in-time access requests (see hscontrol/db/orgscale_access.go).
CREATE TABLE access_requests(
  id integer PRIMARY KEY AUTOINCREMENT,
  user_id integer NOT NULL,
  requester text NOT NULL,
  tag text NOT NULL,
  reason text NOT NULL,
  minutes integer NOT NULL,
  ssh numeric NOT NULL DEFAULT false,
  status text NOT NULL,
  created_at datetime NOT NULL,
  decided_by text,
  decided_at datetime,
  decision_note text,
  expires_at datetime,
  grant_kind text,
  grant_principal text
);
CREATE INDEX idx_access_requests_status ON access_requests(status);

-- orgscale private resources behind subnet routers (see hscontrol/db/orgscale_access.go).
CREATE TABLE network_resources(
  id integer PRIMARY KEY AUTOINCREMENT,
  name text NOT NULL,
  address text NOT NULL,
  description text,
  created_at datetime,
  created_by text
);
