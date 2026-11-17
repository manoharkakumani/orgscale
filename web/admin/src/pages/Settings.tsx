import { useEffect, useState } from "react";
import { del, get, post, put, type GoogleSyncSettings, type GoogleSyncStatus, type ListSetting, type Settings } from "../api";
import { useCanEdit } from "../App";
import { Badge, Button, ChipInput, Confirm, CopyText, ErrorBox, Field, Modal, PageHeader, Spinner, Toggle, dateTime, timeAgo, useAsync, useToast } from "../components/ui";

const emailRe = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;
const domainRe = /^[a-z0-9]([a-z0-9-]*[a-z0-9])?(\.[a-z0-9]([a-z0-9-]*[a-z0-9])?)+$/i;

const validEmail = (v: string) => (emailRe.test(v) ? null : `"${v}" is not an email address`);
const validDomain = (v: string) => (domainRe.test(v.replace(/^@/, "")) ? null : `"${v}" is not a domain, e.g. example.com`);

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
    <Field label={label} hint={hint}>
      <ChipInput values={state.useConfig ? setting.config : state.values} onChange={(values) => onChange({ ...state, values })} disabled={disabled || state.useConfig} placeholder={placeholder} validate={validate} />
      {setting.config.length > 0 && (
        <label className="small muted mt-xs">
          <input type="checkbox" checked={state.useConfig} disabled={disabled} onChange={(e) => onChange({ useConfig: e.target.checked, values: setting.config })} /> Use the configuration file's list
        </label>
      )}
    </Field>
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
  const nobody = !domains.useConfig && !users.useConfig && !groups.useConfig && !domains.values.length && !users.values.length && !groups.values.length;

  return (
    <section className="card section">
      <h2>Who can sign in</h2>
      <p className="muted">
        People must pass every rule that has entries. Leave all three empty to admit anyone your identity provider signs in. Changes apply to the next sign-in; to cut off someone already signed in, disable them on the Users page.
      </p>
      <Rule label="Email domains" hint="e.g. example.com. Only these domains may join." setting={s.sso.allowedDomains} state={domains} onChange={setDomains} placeholder="example.com" validate={validDomain} disabled={!canEdit} />
      <Rule label="Individual people" hint="Only these email addresses may join." setting={s.sso.allowedUsers} state={users} onChange={setUsers} placeholder="alice@example.com" validate={validEmail} disabled={!canEdit} />
      <Rule label="Identity provider groups" hint="Only members of at least one of these groups may join. Needs your IdP to send a groups claim." setting={s.sso.allowedGroups} state={groups} onChange={setGroups} placeholder="vpn-users" disabled={!canEdit} />
      {nobody && <div className="callout callout-warn">No rules: anyone who can sign in to your identity provider can join.</div>}
      {canEdit && (
        <div className="row-end">
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
            Save sign-in rules
          </Button>
        </div>
      )}
    </section>
  );
}

// ---------- roles ----------

function RoleList({ label, hint, s, values, onChange, validate, placeholder, disabled }: { label: string; hint: string; s: ListSetting; values: string[]; onChange: (v: string[]) => void; validate?: (v: string) => string | null; placeholder: string; disabled: boolean }) {
  return (
    <Field label={label} hint={hint}>
      {s.config.length > 0 && (
        <div className="chip-row mb-xs">
          {s.config.map((v) => (
            <span key={v} className="chip chip-static" title="From the configuration file">
              {v} <span className="muted small">config</span>
            </span>
          ))}
        </div>
      )}
      <ChipInput values={values} onChange={onChange} validate={validate} placeholder={placeholder} disabled={disabled} />
    </Field>
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
    <section className="card section">
      <h2>Console access</h2>
      <p className="muted">Who can use this console. Admins can change everything; viewers can look but not change anything. Entries from the configuration file always apply and can only be changed there.</p>
      <RoleList label="Admins (people)" hint="Email addresses." s={s.roles.adminUsers} values={adminUsers} onChange={setAdminUsers} validate={validEmail} placeholder="alice@example.com" disabled={!canEdit} />
      <RoleList label="Admins (groups)" hint="Identity provider groups whose members are admins." s={s.roles.adminGroups} values={adminGroups} onChange={setAdminGroups} placeholder="it-admins" disabled={!canEdit} />
      <RoleList label="Viewers (people)" hint="Read-only access." s={s.roles.viewerUsers} values={viewerUsers} onChange={setViewerUsers} validate={validEmail} placeholder="auditor@example.com" disabled={!canEdit} />
      <RoleList label="Viewers (groups)" hint="Read-only access for members of these groups." s={s.roles.viewerGroups} values={viewerGroups} onChange={setViewerGroups} placeholder="security" disabled={!canEdit} />
      {canEdit && (
        <div className="row-end">
          <Button
            variant="primary"
            busy={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const next = await put<Settings>("/admin/api/settings/roles", { adminUsers, adminGroups, viewerUsers, viewerGroups });
                toast.ok("Console access saved");
                onSaved(next);
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Save console access
          </Button>
        </div>
      )}
    </section>
  );
}

// ---------- device visibility ----------

function Visibility({ s, onChanged }: { s: Settings; onChanged: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  return (
    <section className="card section" id="visibility">
      <h2>Device visibility</h2>
      <p className="muted">
        When restricted, each person's Tailscale client only shows their own devices and the devices assigned to them on the Devices page, directly or through a group. Rules you write in the access policy still apply on top.
      </p>
      <Toggle
        checked={s.deviceRestriction}
        disabled={!canEdit || busy}
        label={s.deviceRestriction ? "Restricted: own and assigned devices only" : "Open: everyone sees every device"}
        onChange={async (enabled) => {
          setBusy(true);
          try {
            await put("/admin/api/settings/device-restriction", { enabled });
            toast.ok(enabled ? "Device visibility restricted" : "Device visibility opened");
            onChanged();
          } catch (e) {
            toast.error(e);
          } finally {
            setBusy(false);
          }
        }}
      />
    </section>
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
    <div className="subsection">
      <div className="section-head">
        <h3>
          SCIM provisioning {s.scim.enabled ? <Badge tone="green">On</Badge> : <Badge>Off</Badge>}
        </h3>
      </div>
      <p className="muted">For Okta, Microsoft Entra ID, OneLogin, JumpCloud and Authentik. Your identity provider pushes users and groups here; deactivating or deleting someone there removes them, their devices and their access here within minutes.</p>
      <Field label="SCIM base URL (tenant URL)">
        <CopyText value={s.scim.baseUrl} />
      </Field>
      {s.scim.enabled && <p className="small muted">Token created {timeAgo(s.scim.createdAt)}.</p>}
      {canEdit && (
        <div className="row">
          <Button variant={s.scim.enabled ? "secondary" : "primary"} busy={busy} onClick={create}>
            {s.scim.enabled ? "Rotate token" : "Turn on and create token"}
          </Button>
          {s.scim.enabled && (
            <Button variant="ghost" className="text-danger" onClick={() => setConfirmRevoke(true)}>
              Turn off
            </Button>
          )}
        </div>
      )}
      <details className="mt">
        <summary>Setup steps</summary>
        <ul className="small">
          <li>
            <b>Okta</b>: Applications → your orgscale app → General → enable SCIM provisioning. Provisioning → Integration: SCIM connector base URL above, unique identifier <code>userName</code>, push new users, profile updates and groups, auth mode <i>HTTP Header</i> with the token. Then To App: enable Create, Update and Deactivate Users.
          </li>
          <li>
            <b>Microsoft Entra ID</b>: Enterprise applications → your orgscale app → Provisioning → Automatic. Tenant URL above, secret token, Test connection, then Start provisioning. Assign users or groups to the app.
          </li>
          <li>
            <b>Authentik</b>: Applications → Providers → create a SCIM provider with the URL and token, then add it as a backchannel provider of the orgscale application.
          </li>
        </ul>
      </details>
      {token && (
        <Modal title="SCIM token" onClose={() => setToken("")} footer={<Button variant="primary" onClick={() => setToken("")}>Done</Button>}>
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
          Your identity provider stops being able to provision. Users and groups already here stay, but people who leave will no longer be removed automatically.
        </Confirm>
      )}
    </div>
  );
}

// ---------- Google Workspace ----------

function SyncStatus({ status }: { status: GoogleSyncStatus }) {
  if (!status?.lastRun || status.lastRun.startsWith("0001")) return <p className="small muted">Never run.</p>;
  return status.ok ? (
    <p className="small">
      <Badge tone="green">OK</Badge> {timeAgo(status.lastRun)}: {status.users} users, {status.groups} groups, {status.created} added, {status.disabled} removed.
    </p>
  ) : (
    <div className="callout callout-error small">
      Failed {timeAgo(status.lastRun)} ({dateTime(status.lastRun)}): {status.error}
    </div>
  );
}

function Google({ s, onChanged }: { s: Settings; onChanged: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const cur = s.googleSync.settings;
  const [form, setForm] = useState<GoogleSyncSettings>({ ...cur, customer: cur.customer || "my_customer", intervalMinutes: cur.intervalMinutes || 15, serviceAccountJson: "" });
  const [busy, setBusy] = useState<"save" | "run" | "">("");

  return (
    <div className="subsection">
      <div className="section-head">
        <h3>Google Workspace sync {cur.enabled ? <Badge tone="green">On</Badge> : <Badge>Off</Badge>}</h3>
      </div>
      <p className="muted">
        Google doesn't push changes to custom apps, so orgscale reads your directory on a schedule. Suspended, archived or deleted Workspace accounts are removed here with their devices. A run that would remove more than half of all users stops instead.
      </p>
      <Toggle checked={form.enabled} disabled={!canEdit} label="Sync from Google Workspace" onChange={(enabled) => setForm({ ...form, enabled })} />
      <Field label="Service account key (JSON)" hint={s.googleSync.hasServiceAccount ? "A key is stored. Paste a new one to replace it." : "Create a service account, enable domain-wide delegation, and download a JSON key."}>
        <textarea rows={3} className="mono small" placeholder={s.googleSync.hasServiceAccount ? "•••• stored ••••" : '{"type": "service_account", ...}'} value={form.serviceAccountJson} disabled={!canEdit} onChange={(e) => setForm({ ...form, serviceAccountJson: e.target.value })} />
      </Field>
      <div className="grid-2">
        <Field label="Admin to impersonate" hint="A Workspace admin email; reads are made as this admin.">
          <input value={form.adminEmail} disabled={!canEdit} onChange={(e) => setForm({ ...form, adminEmail: e.target.value })} placeholder="admin@example.com" />
        </Field>
        <Field label="Customer ID" hint="my_customer means the admin's own organization.">
          <input value={form.customer} disabled={!canEdit} onChange={(e) => setForm({ ...form, customer: e.target.value })} />
        </Field>
      </div>
      <Field label="Sync every (minutes)">
        <input type="number" min={5} max={1440} value={form.intervalMinutes} disabled={!canEdit} onChange={(e) => setForm({ ...form, intervalMinutes: Number(e.target.value) })} />
      </Field>
      <SyncStatus status={s.googleSync.status} />
      {canEdit && (
        <div className="row">
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
        </div>
      )}
      <details className="mt">
        <summary>Setup steps</summary>
        <ol className="small">
          <li>Google Cloud console: create a project, enable the <b>Admin SDK API</b>, create a service account and a JSON key.</li>
          <li>
            Workspace Admin console → Security → API controls → Domain-wide delegation → Add new: the service account's client ID with these scopes:
            <CopyText value="https://www.googleapis.com/auth/admin.directory.user.readonly,https://www.googleapis.com/auth/admin.directory.group.readonly,https://www.googleapis.com/auth/admin.directory.group.member.readonly" />
          </li>
          <li>Paste the key above, set an admin email to impersonate, turn sync on and save.</li>
        </ol>
      </details>
    </div>
  );
}

// ---------- page ----------

export default function SettingsPage() {
  const { data, error, loading, reload, setData } = useAsync(() => get<Settings>("/admin/api/settings"));

  useEffect(() => {
    if (data && window.location.hash) document.getElementById(window.location.hash.slice(1))?.scrollIntoView();
  }, [data]);

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!data) return null;

  return (
    <>
      <PageHeader title="Settings" description="Sign-in, console access, device visibility and directory sync. Changes here take effect without restarting orgscale." />

      <section className="card section">
        <h2>Single sign-on</h2>
        {data.sso.issuer ? (
          <>
            <div className="kv">
              <div>Identity provider</div>
              <div className="mono">{data.sso.issuer}</div>
              <div>Client ID</div>
              <div className="mono">{data.sso.clientId}</div>
            </div>
            <Field label="Redirect URLs to register with your identity provider">
              {data.sso.redirectUrls.map((u) => (
                <CopyText key={u} value={u} />
              ))}
            </Field>
            <p className="small muted">The provider, client ID and secret are set in the configuration file (oidc.*).</p>
          </>
        ) : (
          <div className="callout callout-warn">No identity provider is configured. Set oidc.issuer, oidc.client_id and oidc.client_secret in the configuration file.</div>
        )}
      </section>

      <LoginRules key={JSON.stringify(data.sso)} s={data} onSaved={setData} />
      <Visibility s={data} onChanged={reload} />
      <Roles key={JSON.stringify(data.roles)} s={data} onSaved={setData} />

      <section className="card section" id="directory">
        <h2>Directory sync</h2>
        <p className="muted">Keep users and groups in step with your identity provider, so new people get their groups' access and people who leave lose access automatically. Use SCIM or Google Workspace.</p>
        <SCIM s={data} onChanged={reload} />
        <Google key={JSON.stringify(data.googleSync.settings)} s={data} onChanged={reload} />
      </section>
    </>
  );
}
