import { useState } from "react";
import { useSearchParams } from "react-router-dom";
import {
  del,
  get,
  post,
  put,
  type AdminSessionInfo,
  type EntraSyncSettings,
  type EntraSyncStatus,
  type GoogleSyncSettings,
  type GoogleSyncStatus,
  type ListSetting,
  type Settings,
  type SSOState,
} from "../api";
import { SSOFields, initialForm, issuerFor } from "../components/sso";
import DNSEditor from "../components/dns";
import TailnetLinks from "../components/tailnets";
import { useCanEdit } from "../App";
import {
  Badge,
  Button,
  ChipInput,
  Confirm,
  CopyText,
  ErrorBox,
  Group,
  Icon,
  Modal,
  PRow,
  PageHeader,
  Spinner,
  Toggle,
  dateTime,
  timeAgo,
  useAsync,
  useToast,
} from "../components/ui";

const emailRe = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const domainRe = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i;

const validEmail = (v: string) => (emailRe.test(v) ? null : `"${v}" is not an email address`);
const validDomain = (v: string) =>
  domainRe.test(v.replace(/^@/, "")) ? null : `"${v}" is not a domain, e.g. example.com`;

// ---------- login rules ----------

type RuleState = { useConfig: boolean; values: string[] };

// A rule follows the configuration file only while the console has not set
// it and the file has a list; otherwise it is edited here.
function ruleFrom(s: ListSetting): RuleState {
  return { useConfig: s.console === null && s.config.length > 0, values: s.console ?? s.config };
}

function Rule({
  label,
  hint,
  setting,
  state,
  onChange,
  placeholder,
  validate,
  disabled,
}: {
  label: string;
  hint: string;
  setting: ListSetting;
  state: RuleState;
  onChange: (s: RuleState) => void;
  placeholder: string;
  validate?: (v: string) => string | null;
  disabled: boolean;
}) {
  return (
    <PRow label={label} sub={hint} block>
      <ChipInput
        values={state.useConfig ? setting.config : state.values}
        onChange={(values) => onChange({ ...state, values })}
        disabled={disabled || state.useConfig}
        placeholder={placeholder}
        validate={validate}
      />
      {setting.config.length > 0 && (
        <label className="small muted check-row">
          <input
            type="checkbox"
            checked={state.useConfig}
            disabled={disabled}
            onChange={(e) => onChange({ useConfig: e.target.checked, values: setting.config })}
          />{" "}
          Use the configuration file's list
        </label>
      )}
    </PRow>
  );
}

function LoginRules({ s, onSaved }: { s: Settings; onSaved: (s: Settings) => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const [domains, setDomains] = useState(ruleFrom(s.sso.allowedDomains));
  const [users, setUsers] = useState(ruleFrom(s.sso.allowedUsers));
  const [groups, setGroups] = useState(ruleFrom(s.sso.allowedGroups));
  const [busy, setBusy] = useState(false);

  const body = (r: RuleState) => (r.useConfig ? null : r.values);
  const nobody =
    !domains.useConfig &&
    !users.useConfig &&
    !groups.useConfig &&
    !domains.values.length &&
    !users.values.length &&
    !groups.values.length;

  return (
    <>
      <Group note="People must pass every rule that has entries. Changes apply from the next sign-in; to cut off someone already connected, disable them on the Users page.">
        <Rule
          label="Email domains"
          hint="e.g. example.com. Only these domains may join."
          setting={s.sso.allowedDomains}
          state={domains}
          onChange={setDomains}
          placeholder="example.com"
          validate={validDomain}
          disabled={!canEdit}
        />
        <Rule
          label="Individual people"
          hint="Only these email addresses may join."
          setting={s.sso.allowedUsers}
          state={users}
          onChange={setUsers}
          placeholder="alice@example.com"
          validate={validEmail}
          disabled={!canEdit}
        />
        <Rule
          label="Identity provider groups"
          hint="Only members of at least one of these groups may join. Needs your IdP to send a groups claim."
          setting={s.sso.allowedGroups}
          state={groups}
          onChange={setGroups}
          placeholder="vpn-users"
          disabled={!canEdit}
        />
      </Group>
      {nobody && (
        <div className="callout callout-warn mt">
          No rules: anyone who can sign in to your identity provider can join.
        </div>
      )}
      {canEdit && (
        <div className="savebar">
          <Button
            variant="primary"
            busy={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const next = await put<Settings>("/admin/api/settings/login-rules", {
                  allowedDomains: body(domains),
                  allowedUsers: body(users),
                  allowedGroups: body(groups),
                });
                toast.ok("Sign-in rules saved");
                onSaved(next);
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Save
          </Button>
        </div>
      )}
    </>
  );
}

// ---------- roles ----------

function RoleList({
  label,
  hint,
  s,
  values,
  onChange,
  validate,
  placeholder,
  disabled,
}: {
  label: string;
  hint: string;
  s: ListSetting;
  values: string[];
  onChange: (v: string[]) => void;
  validate?: (v: string) => string | null;
  placeholder: string;
  disabled: boolean;
}) {
  return (
    <PRow label={label} sub={hint} block>
      {s.config.length > 0 && (
        <div className="chip-row mb-xs">
          {s.config.map((v) => (
            <span key={v} className="chip chip-static" title="From the configuration file">
              {v} <span className="muted small">config</span>
            </span>
          ))}
        </div>
      )}
      <ChipInput
        values={values}
        onChange={onChange}
        validate={validate}
        placeholder={placeholder}
        disabled={disabled}
      />
    </PRow>
  );
}

function Roles({ s, onSaved }: { s: Settings; onSaved: (s: Settings) => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const [adminUsers, setAdminUsers] = useState(s.roles.adminUsers.console ?? []);
  const [adminGroups, setAdminGroups] = useState(s.roles.adminGroups.console ?? []);
  const [viewerUsers, setViewerUsers] = useState(s.roles.viewerUsers.console ?? []);
  const [viewerGroups, setViewerGroups] = useState(s.roles.viewerGroups.console ?? []);
  const [busy, setBusy] = useState(false);

  return (
    <>
      <Group title="Administrators" note="Admins can change everything.">
        <RoleList
          label="People"
          hint="Email addresses."
          s={s.roles.adminUsers}
          values={adminUsers}
          onChange={setAdminUsers}
          validate={validEmail}
          placeholder="alice@example.com"
          disabled={!canEdit}
        />
        <RoleList
          label="Groups"
          hint="Identity provider groups whose members are admins."
          s={s.roles.adminGroups}
          values={adminGroups}
          onChange={setAdminGroups}
          placeholder="it-admins"
          disabled={!canEdit}
        />
      </Group>
      <Group
        title="Viewers"
        note="Viewers can look at everything but change nothing. Entries marked config come from the configuration file and can only be changed there."
      >
        <RoleList
          label="People"
          hint="Read-only access."
          s={s.roles.viewerUsers}
          values={viewerUsers}
          onChange={setViewerUsers}
          validate={validEmail}
          placeholder="auditor@example.com"
          disabled={!canEdit}
        />
        <RoleList
          label="Groups"
          hint="Read-only access for members of these groups."
          s={s.roles.viewerGroups}
          values={viewerGroups}
          onChange={setViewerGroups}
          placeholder="security"
          disabled={!canEdit}
        />
      </Group>
      {canEdit && (
        <div className="savebar">
          <Button
            variant="primary"
            busy={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const next = await put<Settings>("/admin/api/settings/roles", {
                  adminUsers,
                  adminGroups,
                  viewerUsers,
                  viewerGroups,
                });
                toast.ok("Console access saved");
                onSaved(next);
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Save
          </Button>
        </div>
      )}
    </>
  );
}

// ---------- SCIM ----------

function SCIM({ s, onChanged }: { s: Settings; onChanged: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const [token, setToken] = useState("");
  const [busy, setBusy] = useState(false);
  const [confirmRevoke, setConfirmRevoke] = useState(false);

  const create = async () => {
    setBusy(true);
    try {
      const res = await post<{ token: string }>("/admin/api/settings/scim-token");
      setToken(res.token);
      onChanged();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <div>
      <Group>
        <PRow
          label="SCIM provisioning"
          sub={s.scim.enabled ? `On · token created ${timeAgo(s.scim.createdAt)}` : "Off"}
        >
          {s.scim.enabled ? <Badge tone="green">On</Badge> : <Badge>Off</Badge>}
          {canEdit && (
            <Button variant={s.scim.enabled ? "secondary" : "primary"} busy={busy} onClick={create}>
              {s.scim.enabled ? "Rotate token" : "Turn on"}
            </Button>
          )}
          {canEdit && s.scim.enabled && (
            <Button variant="ghost" className="text-danger" onClick={() => setConfirmRevoke(true)}>
              Turn off
            </Button>
          )}
        </PRow>
        <PRow label="SCIM base URL" sub="Also called tenant URL or connector base URL." block>
          <CopyText value={s.scim.baseUrl} />
        </PRow>
      </Group>
      <details className="disclosure">
        <summary>Setup steps for Okta, Entra ID and Authentik</summary>
        <ul className="small">
          <li>
            <b>Okta</b>: Applications → your orgscale app → General → enable SCIM provisioning. Provisioning →
            Integration: SCIM connector base URL above, unique identifier <code>userName</code>, push new users, profile
            updates and groups, auth mode <i>HTTP Header</i> with the token. Then To App: enable Create, Update and
            Deactivate Users.
          </li>
          <li>
            <b>Microsoft Entra ID</b>: Enterprise applications → your orgscale app → Provisioning → Automatic. Tenant
            URL above, secret token, Test connection, then Start provisioning. Assign users or groups to the app.
          </li>
          <li>
            <b>Authentik</b>: Applications → Providers → create a SCIM provider with the URL and token, then add it as a
            backchannel provider of the orgscale application.
          </li>
        </ul>
      </details>
      {token && (
        <Modal
          title="SCIM token"
          onClose={() => setToken("")}
          footer={
            <Button variant="primary" onClick={() => setToken("")}>
              Done
            </Button>
          }
        >
          <p>Paste this into your identity provider now. It is not shown again; any previous token stops working.</p>
          <CopyText value={token} />
        </Modal>
      )}
      {confirmRevoke && (
        <Confirm
          title="Turn off SCIM?"
          confirmLabel="Turn off"
          danger
          onClose={() => setConfirmRevoke(false)}
          onConfirm={async () => {
            await del("/admin/api/settings/scim-token");
            toast.ok("SCIM turned off");
            onChanged();
          }}
        >
          Your identity provider stops being able to provision. Users and groups already here stay, but people who leave
          will no longer be removed automatically.
        </Confirm>
      )}
    </div>
  );
}

// ---------- Google Workspace ----------

function SyncStatus({ status }: { status: GoogleSyncStatus }) {
  if (!status?.lastRun || status.lastRun.startsWith("0001")) return <span className="muted">Never</span>;
  return status.ok ? (
    <span className="small">
      <Badge tone="green">OK</Badge> {timeAgo(status.lastRun)} · {status.users} users, {status.groups} groups,{" "}
      {status.created} added, {status.disabled} removed
    </span>
  ) : (
    <span className="small text-danger" title={dateTime(status.lastRun)}>
      Failed {timeAgo(status.lastRun)}: {status.error}
    </span>
  );
}

function Google({ s, onChanged }: { s: Settings; onChanged: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const cur = s.googleSync.settings;
  const [form, setForm] = useState<GoogleSyncSettings>({
    ...cur,
    customer: cur.customer || "my_customer",
    intervalMinutes: cur.intervalMinutes || 15,
    serviceAccountJson: "",
  });
  const [busy, setBusy] = useState<"save" | "run" | "">("");

  return (
    <div>
      <Group note="A run that would remove more than 25 users, or more than half of all users, stops instead.">
        <PRow
          label="Sync from Google Workspace"
          sub="Suspended, archived or deleted accounts are removed here with their devices."
        >
          <Toggle
            checked={form.enabled}
            disabled={!canEdit}
            label=""
            onChange={(enabled) => setForm({ ...form, enabled })}
          />
        </PRow>
        <PRow
          label="Service account key"
          sub={
            s.googleSync.hasServiceAccount
              ? "A key is stored. Paste a new one to replace it."
              : "JSON key of a service account with domain-wide delegation."
          }
          block
        >
          <textarea
            rows={3}
            className="mono small"
            placeholder={s.googleSync.hasServiceAccount ? "•••• stored ••••" : '{"type": "service_account", ...}'}
            value={form.serviceAccountJson}
            disabled={!canEdit}
            onChange={(e) => setForm({ ...form, serviceAccountJson: e.target.value })}
          />
        </PRow>
        <PRow label="Admin to impersonate" sub="Reads are made as this Workspace super admin.">
          <input
            style={{ width: 260 }}
            value={form.adminEmail}
            disabled={!canEdit}
            onChange={(e) => setForm({ ...form, adminEmail: e.target.value })}
            placeholder="admin@example.com"
          />
        </PRow>
        <PRow label="Customer ID" sub="my_customer means the admin's own organization.">
          <input
            style={{ width: 260 }}
            value={form.customer}
            disabled={!canEdit}
            onChange={(e) => setForm({ ...form, customer: e.target.value })}
          />
        </PRow>
        <PRow label="Sync every" sub="Minutes between runs.">
          <input
            style={{ width: 90 }}
            type="number"
            min={5}
            max={1440}
            value={form.intervalMinutes}
            disabled={!canEdit}
            onChange={(e) => setForm({ ...form, intervalMinutes: Number(e.target.value) })}
          />
        </PRow>
        <PRow label="Last run">
          <SyncStatus status={s.googleSync.status} />
        </PRow>
      </Group>
      {canEdit && (
        <div className="savebar">
          <Button
            busy={busy === "run"}
            disabled={!cur.enabled}
            onClick={async () => {
              setBusy("run");
              try {
                const st = await post<GoogleSyncStatus>("/admin/api/settings/google-sync/run");
                if (st.ok) toast.ok(`Synced ${st.users} users and ${st.groups} groups`);
                else toast.error(st.error ?? "Sync failed");
                onChanged();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy("");
              }
            }}
          >
            Sync now
          </Button>
          <Button
            variant="primary"
            busy={busy === "save"}
            onClick={async () => {
              setBusy("save");
              try {
                await put("/admin/api/settings/google-sync", form);
                toast.ok("Google sync saved");
                setForm({ ...form, serviceAccountJson: "" });
                onChanged();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy("");
              }
            }}
          >
            Save
          </Button>
        </div>
      )}
      <details className="disclosure">
        <summary>Setup steps</summary>
        <ol className="small">
          <li>
            Google Cloud console: create a project, enable the <b>Admin SDK API</b>, create a service account and a JSON
            key.
          </li>
          <li>
            Workspace Admin console → Security → API controls → Domain-wide delegation → Add new: the service account's
            client ID with these scopes:
            <CopyText value="https://www.googleapis.com/auth/admin.directory.user.readonly,https://www.googleapis.com/auth/admin.directory.group.readonly,https://www.googleapis.com/auth/admin.directory.group.member.readonly" />
          </li>
          <li>Paste the key above, set an admin email to impersonate, turn sync on and save.</li>
        </ol>
      </details>
    </div>
  );
}

// ---------- single sign-on ----------

function SSOEditor() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<SSOState>("/admin/api/settings/sso"));
  const [form, setForm] = useState(initialForm());
  const [editing, setEditing] = useState(false);
  const [busy, setBusy] = useState(false);

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!data) return null;

  if (!editing) {
    return (
      <>
        <Group>
          <PRow label="Identity provider" sub={data.fromConfig ? "Set in the configuration file (oidc.*)." : undefined}>
            <span className="mono ellipsis">{data.issuer || "not set up"}</span>
          </PRow>
          <PRow label="Client ID">
            <span className="mono ellipsis">{data.clientId || "—"}</span>
          </PRow>
        </Group>
        <Group
          title="Redirect URLs"
          note="Registered with your identity provider: the first for devices, the second for this console."
        >
          {data.redirectUrls.map((u, i) => (
            <PRow key={u} label={i === 0 ? "Devices" : "Admin console"} block>
              <CopyText value={u} />
            </PRow>
          ))}
        </Group>
        {canEdit && !data.fromConfig && (
          <div className="savebar">
            <Button
              onClick={() => {
                setForm(initialForm(data));
                setEditing(true);
              }}
            >
              Change identity provider
            </Button>
          </div>
        )}
      </>
    );
  }

  return (
    <>
      <div className="callout callout-warn">
        Changing the provider signs people in through the new one from now on. Make sure your own account works there,
        or you can lock yourself out.
      </div>
      <SSOFields state={data} form={form} onChange={setForm} />
      <div className="savebar">
        <Button variant="ghost" onClick={() => setEditing(false)}>
          Cancel
        </Button>
        <Button
          variant="primary"
          busy={busy}
          disabled={!issuerFor(form) || !form.clientId.trim()}
          onClick={async () => {
            setBusy(true);
            try {
              await put("/admin/api/settings/sso", {
                issuer: issuerFor(form),
                clientId: form.clientId,
                clientSecret: form.clientSecret,
              });
              toast.ok("Single sign-on updated");
              setEditing(false);
              void reload();
            } catch (e) {
              toast.error(e);
            } finally {
              setBusy(false);
            }
          }}
        >
          Check and save
        </Button>
      </div>
    </>
  );
}

// ---------- console sessions ----------

function browserName(ua: string): string {
  const b = /Edg\//.test(ua)
    ? "Edge"
    : /Chrome\//.test(ua)
      ? "Chrome"
      : /Firefox\//.test(ua)
        ? "Firefox"
        : /Safari\//.test(ua)
          ? "Safari"
          : "Browser";
  const os = /iPhone|iPad/.test(ua)
    ? "iOS"
    : /Android/.test(ua)
      ? "Android"
      : /Mac OS X/.test(ua)
        ? "macOS"
        : /Windows/.test(ua)
          ? "Windows"
          : /Linux/.test(ua)
            ? "Linux"
            : "";
  return os ? `${b} on ${os}` : b;
}

function Sessions() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, reload } = useAsync(() => get<AdminSessionInfo[]>("/admin/api/sessions"));
  const others = (data ?? []).filter((s) => !s.current).length;

  const end = async (id: string, msg: string) => {
    try {
      await del(`/admin/api/sessions/${id}`);
      toast.ok(msg);
      void reload();
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <>
      <Group
        title="Signed-in sessions"
        note="Sessions last until they expire or someone signs them out, also across server restarts."
      >
        {(data ?? []).map((s) => (
          <PRow
            key={s.id}
            label={
              <>
                {s.email} {s.current && <Badge tone="green">This browser</Badge>}
              </>
            }
            sub={`${browserName(s.userAgent)} · ${s.remoteAddr || "unknown address"} · active ${timeAgo(s.lastSeenAt)} · signed in ${timeAgo(s.createdAt)}`}
          >
            {canEdit && !s.current && (
              <Button variant="ghost" className="text-danger" onClick={() => void end(s.id, "Session signed out")}>
                Sign out
              </Button>
            )}
          </PRow>
        ))}
      </Group>
      {canEdit && others > 0 && (
        <div className="savebar">
          <Button onClick={() => void end("others", "Signed out of your other sessions")}>
            Sign out my other sessions
          </Button>
        </div>
      )}
    </>
  );
}

// ---------- Microsoft Entra ID ----------

const graphPermissions = ["User.Read.All", "Group.Read.All", "GroupMember.Read.All", "Organization.Read.All"];

function EntraStatus({ s }: { s: EntraSyncStatus }) {
  if (!s?.lastRun || s.lastRun.startsWith("0001")) return <span className="muted">Never</span>;
  if (!s.ok)
    return (
      <span className="small text-danger" title={dateTime(s.lastRun)}>
        Failed {timeAgo(s.lastRun)}: {s.error}
      </span>
    );
  return (
    <span className="small">
      <Badge tone="green">OK</Badge> checked {timeAgo(s.lastRun)}
      {s.unchanged
        ? " · no changes"
        : ` · ${s.users} users, ${s.groups} groups, ${s.created} added, ${s.disabled} removed`}
      {s.warning && <div className="text-danger small">{s.warning}</div>}
    </span>
  );
}

function Entra({ s, onChanged }: { s: Settings; onChanged: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const cur = s.entraSync;
  const [form, setForm] = useState<EntraSyncSettings>({
    ...cur.settings,
    intervalMinutes: cur.settings.intervalMinutes || 15,
    clientSecret: "",
  });
  const [ownApp, setOwnApp] = useState(!!cur.settings.clientId || !cur.usesSSOApp);
  const [busy, setBusy] = useState<"save" | "run" | "">("");

  return (
    <div>
      <Group note="Each check asks Microsoft Graph only what changed since the last one: two small requests when nothing did. The directory is read in full when something changed, and once a day.">
        <PRow
          label="Sync from Microsoft Entra ID"
          sub="Members are added, people disabled or deleted in Entra are removed here with their devices, and groups follow Entra. Guests are not synced."
        >
          <Toggle
            checked={form.enabled}
            disabled={!canEdit}
            label="Sync from Microsoft Entra ID"
            hideLabel
            onChange={(enabled) => setForm({ ...form, enabled })}
          />
        </PRow>
        <PRow label="Check for changes every">
          <select
            value={form.intervalMinutes}
            disabled={!canEdit}
            style={{ width: 150 }}
            onChange={(e) => setForm({ ...form, intervalMinutes: Number(e.target.value) })}
          >
            {[5, 15, 30, 60, 240, 1440].map((m) => (
              <option key={m} value={m}>
                {m < 60 ? `${m} minutes` : m === 1440 ? "day" : `${m / 60} hour${m === 60 ? "" : "s"}`}
              </option>
            ))}
          </select>
        </PRow>
        <PRow label="Last check">
          <EntraStatus s={cur.status} />
        </PRow>
      </Group>

      <Group title="App registration">
        {cur.usesSSOApp && (
          <PRow
            label="Use the single sign-on app"
            sub={`Tenant ${cur.tenantId}, client ${cur.clientId}. Nothing else to set up apart from the permissions below.`}
          >
            <Toggle
              checked={!ownApp}
              disabled={!canEdit}
              label="Use the single sign-on app"
              hideLabel
              onChange={(v) => setOwnApp(!v)}
            />
          </PRow>
        )}
        {ownApp && (
          <>
            <PRow label="Directory (tenant) ID">
              <input
                value={form.tenantId ?? ""}
                disabled={!canEdit}
                placeholder="00000000-0000-…"
                onChange={(e) => setForm({ ...form, tenantId: e.target.value })}
              />
            </PRow>
            <PRow label="Application (client) ID">
              <input
                value={form.clientId ?? ""}
                disabled={!canEdit}
                onChange={(e) => setForm({ ...form, clientId: e.target.value })}
              />
            </PRow>
            <PRow label="Client secret" sub={cur.hasSecret ? "Stored. Leave empty to keep it." : undefined}>
              <input
                type="password"
                value={form.clientSecret ?? ""}
                disabled={!canEdit}
                placeholder={cur.hasSecret ? "••••••••" : ""}
                autoComplete="new-password"
                onChange={(e) => setForm({ ...form, clientSecret: e.target.value })}
              />
            </PRow>
          </>
        )}
      </Group>

      <details className="disclosure" open={!cur.status?.ok}>
        <summary>Permissions the app needs</summary>
        <ol className="small">
          <li>
            In entra.microsoft.com open <b>App registrations</b> → your orgscale app → <b>API permissions</b>.
          </li>
          <li>
            <b>Add a permission → Microsoft Graph → Application permissions</b>, and add:{" "}
            {graphPermissions.map((p) => (
              <code key={p} style={{ marginRight: 4 }}>
                {p}
              </code>
            ))}
          </li>
          <li>
            Check the <b>Type</b> column says <b>Application</b>. <b>Delegated</b> permissions (the ones sign-in uses,
            like <code>openid</code> and <code>User.Read</code>) do not work for sync.
          </li>
          <li>
            Click <b>Grant admin consent</b> for your organization.
          </li>
          <li>
            Turn sync on here and click <b>Sync now</b>.
          </li>
        </ol>
      </details>

      {canEdit && (
        <div className="savebar">
          <Button
            busy={busy === "run"}
            disabled={!cur.settings.enabled}
            onClick={async () => {
              setBusy("run");
              try {
                const st = await post<EntraSyncStatus>("/admin/api/settings/entra-sync/run");
                if (st.ok) toast.ok(`Synced ${st.users} users and ${st.groups} groups`);
                else toast.error(st.error ?? "Sync failed");
                onChanged();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy("");
              }
            }}
          >
            Sync now
          </Button>
          <Button
            variant="primary"
            busy={busy === "save"}
            onClick={async () => {
              setBusy("save");
              try {
                const body = ownApp ? form : { enabled: form.enabled, intervalMinutes: form.intervalMinutes };
                await put("/admin/api/settings/entra-sync", body);
                toast.ok("Microsoft Entra sync saved");
                setForm({ ...form, clientSecret: "" });
                onChanged();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy("");
              }
            }}
          >
            Save
          </Button>
        </div>
      )}
    </div>
  );
}

// ---------- page ----------

const sections = [
  {
    id: "sso",
    label: "Single sign-on",
    tile: "tile-blue",
    icon: "sso",
    text: "Your identity provider and the redirect URLs to register with it.",
  },
  {
    id: "signin",
    label: "Who can sign in",
    tile: "tile-green",
    icon: "user",
    text: "Email domains, people and groups allowed to join the network.",
  },
  {
    id: "console",
    label: "Console access",
    tile: "tile-violet",
    icon: "key",
    text: "Who can use this console, as an admin or read-only.",
  },
  {
    id: "dns",
    label: "DNS",
    tile: "tile-orange",
    icon: "globe",
    text: "Device names (MagicDNS), the DNS servers devices use, split DNS for internal domains and custom records.",
  },
  {
    id: "tailnets",
    label: "Tailnets",
    tile: "tile-violet",
    icon: "link",
    text: "Link the consoles of your other orgscale tailnets, such as staging or another region.",
  },
  {
    id: "entra",
    label: "Microsoft Entra ID",
    tile: "tile-blue",
    icon: "sync",
    text: "Read users and groups from Microsoft Entra ID through Microsoft Graph.",
  },
  {
    id: "directory",
    label: "SCIM provisioning",
    tile: "tile-teal",
    icon: "link",
    text: "Okta, Microsoft Entra ID, OneLogin, JumpCloud and Authentik push users and groups here.",
  },
  {
    id: "google",
    label: "Google Workspace",
    tile: "tile-orange",
    icon: "sync",
    text: "Read users and groups from Google Workspace on a schedule.",
  },
] as const;

type SectionId = (typeof sections)[number]["id"];

export default function SettingsPage() {
  const { data, error, loading, reload, setData } = useAsync(() => get<Settings>("/admin/api/settings"));
  const [params, setParams] = useSearchParams();
  const current = (sections.find((x) => x.id === params.get("section"))?.id ?? "sso") as SectionId;
  const sec = sections.find((x) => x.id === current)!;

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!data) return null;

  const status: Partial<Record<SectionId, string>> = {
    entra: data.entraSync.settings.enabled ? "On" : "Off",
    directory: data.scim.enabled ? "On" : "Off",
    google: data.googleSync.settings.enabled ? "On" : "Off",
  };

  return (
    <>
      <PageHeader title="Settings" description="Changes take effect immediately, without restarting orgscale." />
      <div className="split">
        <nav className="split-nav" aria-label="Settings sections">
          <div className="nav-group">Identity</div>
          {sections.slice(0, 3).map((x) => (
            <button
              key={x.id}
              className={current === x.id ? "active" : ""}
              onClick={() => setParams({ section: x.id })}
            >
              <span className={`nav-tile ${x.tile}`}>
                <Icon name={x.icon} />
              </span>
              {x.label}
            </button>
          ))}
          <div className="nav-group">Network</div>
          {sections.slice(3, 5).map((x) => (
            <button
              key={x.id}
              className={current === x.id ? "active" : ""}
              onClick={() => setParams({ section: x.id })}
            >
              <span className={`nav-tile ${x.tile}`}>
                <Icon name={x.icon} />
              </span>
              {x.label}
            </button>
          ))}
          <div className="nav-group">Directory sync</div>
          {sections.slice(5).map((x) => (
            <button
              key={x.id}
              className={current === x.id ? "active" : ""}
              onClick={() => setParams({ section: x.id })}
            >
              <span className={`nav-tile ${x.tile}`}>
                <Icon name={x.icon} />
              </span>
              <span className="grow">{x.label}</span>
              <span className="small" style={{ opacity: 0.7 }}>
                {status[x.id]}
              </span>
            </button>
          ))}
        </nav>

        <div>
          <div className="split-head">
            <span className={`nav-tile ${sec.tile}`}>
              <Icon name={sec.icon} />
            </span>
            <div>
              <h2 style={{ margin: 0, fontSize: 17 }}>{sec.label}</h2>
              <p className="muted">{sec.text}</p>
            </div>
          </div>

          {current === "sso" && <SSOEditor />}
          {current === "dns" && <DNSEditor />}
          {current === "tailnets" && <TailnetLinks />}
          {current === "signin" && <LoginRules key={JSON.stringify(data.sso)} s={data} onSaved={setData} />}
          {current === "console" && (
            <>
              <Roles key={JSON.stringify(data.roles)} s={data} onSaved={setData} />
              <Sessions />
            </>
          )}
          {current === "directory" && <SCIM s={data} onChanged={reload} />}
          {current === "entra" && <Entra key={JSON.stringify(data.entraSync.settings)} s={data} onChanged={reload} />}
          {current === "google" && (
            <Google key={JSON.stringify(data.googleSync.settings)} s={data} onChanged={reload} />
          )}
        </div>
      </div>
    </>
  );
}
