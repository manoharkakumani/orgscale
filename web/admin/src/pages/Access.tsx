import { useState } from "react";
import { Link } from "react-router-dom";
import { del, get, post, put, type AccessRule, type Overview, type Principal } from "../api";
import { useCanEdit } from "../App";
import { PrincipalList, PrincipalPicker } from "../components/principals";
import { Badge, Button, ChipInput, Confirm, Empty, ErrorBox, Field, Icon, Modal, PageHeader, Segmented, Spinner, Toggle, timeAgo, useAsync, useToast } from "../components/ui";
import { PolicyEditor } from "./Policy";

const ruleKinds = ["everyone", "directory_group", "policy_group", "user", "tag", "device"] as const;
const sshWhoKinds = ["everyone", "directory_group", "policy_group", "user"] as const;
const sshWhereKinds = ["tag", "self"] as const;

const checkPeriods = [
  { value: "", label: "Off" },
  { value: "1h", label: "1 hour" },
  { value: "12h", label: "12 hours" },
  { value: "24h", label: "1 day" },
  { value: "168h", label: "7 days" },
];

const nonRoot = "autogroup:nonroot";
const loginRe = /^[a-z_][a-z0-9_.-]{0,31}$/;

export function describeLogins(users: string[]): string {
  return users.map((u) => (u === nonRoot ? "any non-root account" : u)).join(", ");
}

const presets: { label: string; ports: string; protocol: AccessRule["protocol"] }[] = [
  { label: "Everything", ports: "*", protocol: "" },
  { label: "SSH", ports: "22", protocol: "tcp" },
  { label: "Web", ports: "80,443", protocol: "tcp" },
  { label: "Remote Desktop", ports: "3389", protocol: "tcp" },
  { label: "PostgreSQL", ports: "5432", protocol: "tcp" },
  { label: "MySQL", ports: "3306", protocol: "tcp" },
  { label: "Ping", ports: "*", protocol: "icmp" },
];

export function describePorts(protocol: string, ports: string): string {
  const preset = presets.find((p) => p.ports === ports && p.protocol === protocol);
  if (preset) return preset.label === "Everything" ? "All ports" : `${preset.label} (${protocol === "icmp" ? "ICMP" : ports})`;
  const p = ports === "*" ? "all ports" : `port${ports.includes(",") || ports.includes("-") ? "s" : ""} ${ports.replace(/,/g, ", ")}`;
  return protocol ? `${protocol.toUpperCase()} ${p}` : p.charAt(0).toUpperCase() + p.slice(1);
}

type Draft = {
  id?: number;
  kind: AccessRule["kind"];
  name: string;
  enabled: boolean;
  sources: Principal[];
  destinations: Principal[];
  protocol: AccessRule["protocol"];
  ports: string;
  sshUsers: string[];
  sshCheckPeriod: string;
};

const ruleBody = (d: Draft) => ({
  kind: d.kind,
  name: d.name,
  enabled: d.enabled,
  protocol: d.protocol,
  ports: d.ports,
  sshUsers: d.sshUsers,
  sshCheckPeriod: d.sshCheckPeriod,
  sources: d.sources.map(({ kind, principal }) => ({ kind, principal })),
  destinations: d.destinations.map(({ kind, principal }) => ({ kind, principal })),
});

function RuleEditor({ initial, onClose, onSaved }: { initial: Draft; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [d, setD] = useState<Draft>(initial);
  const [busy, setBusy] = useState(false);
  const preset = presets.find((p) => p.ports === d.ports && p.protocol === d.protocol);
  const [custom, setCustom] = useState(!preset);

  const autoName = () => {
    const who = d.sources[0]?.label ?? "";
    const what = d.destinations[0]?.label ?? "";
    return who && what ? `${who} → ${what}` : "";
  };

  const save = async () => {
    setBusy(true);
    try {
      const body = ruleBody({ ...d, name: d.name.trim() || autoName() });
      if (d.id) await put(`/admin/api/rules/${d.id}`, body);
      else await post("/admin/api/rules", body);
      toast.ok(d.id ? "Rule updated" : "Rule added");
      onSaved();
      onClose();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title={d.id ? "Edit rule" : d.kind === "ssh" ? "New SSH rule" : "New access rule"}
      onClose={onClose}
      xl
      footer={
        <>
          <Toggle checked={d.enabled} onChange={(enabled) => setD({ ...d, enabled })} label={d.enabled ? "Active" : "Paused"} />
          <span className="grow" />
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={busy} disabled={!d.sources.length || !d.destinations.length || (d.kind === "ssh" && !d.sshUsers.length)} onClick={save}>
            {d.id ? "Save rule" : "Add rule"}
          </Button>
        </>
      }
    >
      {!d.id && (
        <Field label="Type">
          <Segmented
            value={d.kind}
            options={[
              { value: "network", label: "Network access" },
              { value: "ssh", label: "SSH" },
            ]}
            onChange={(kind) => setD({ ...d, kind, sources: [], destinations: [], sshUsers: kind === "ssh" ? ["ubuntu"] : [] })}
          />
        </Field>
      )}
      <Field label="Name">
        <input value={d.name} placeholder={autoName() || "e.g. Engineering can reach staging servers"} onChange={(e) => setD({ ...d, name: e.target.value })} autoFocus />
      </Field>

      <div className="builder">
        <div className="builder-col">
          <div className="field-label">Who</div>
          <PrincipalPicker kinds={d.kind === "ssh" ? [...sshWhoKinds] : [...ruleKinds]} value={d.sources} onChange={(sources) => setD({ ...d, sources })} height={220} />
        </div>
        <div className="builder-arrow">
          <Icon name="arrow" />
        </div>
        <div className="builder-col">
          <div className="field-label">{d.kind === "ssh" ? "Can SSH into" : "Can reach"}</div>
          <PrincipalPicker
            kinds={d.kind === "ssh" ? [...sshWhereKinds] : [...ruleKinds]}
            value={d.destinations}
            onChange={(destinations) => setD({ ...d, destinations })}
            placeholder={d.kind === "ssh" ? "Search server tags" : undefined}
            height={220}
          />
        </div>
      </div>

      {d.kind === "ssh" ? (
        <>
          <Field label="As Linux accounts" hint="The accounts people may log in as on those servers. Tailscale SSH checks who they are; no SSH keys are needed.">
            <ChipInput
              values={d.sshUsers}
              onChange={(sshUsers) => setD({ ...d, sshUsers })}
              placeholder="ubuntu"
              suggestions={["root", "ubuntu", "ec2-user", "admin", "debian", nonRoot]}
              validate={(v) => (v === nonRoot || loginRe.test(v) ? null : `"${v}" is not a Linux account name`)}
            />
            <label className="check-row small">
              <input
                type="checkbox"
                checked={d.sshUsers.includes(nonRoot)}
                onChange={(e) => setD({ ...d, sshUsers: e.target.checked ? [...d.sshUsers, nonRoot] : d.sshUsers.filter((u) => u !== nonRoot) })}
              />
              Also any non-root account
            </label>
          </Field>
          <Field label="Re-confirm with single sign-on" hint="Ask people to sign in again (with MFA) before an SSH session, then again after this long. Not available when a server tag is in Who.">
            <Segmented value={d.sshCheckPeriod} options={checkPeriods} onChange={(sshCheckPeriod) => setD({ ...d, sshCheckPeriod })} />
          </Field>
          <div className="field-hint">
            {d.sources.length && d.destinations.length && d.sshUsers.length ? (
              <>
                <b>{d.sources.map((s) => s.label || (s.kind === "everyone" ? "Everyone" : s.principal)).join(", ")}</b> can SSH into{" "}
                <b>{d.destinations.map((s) => (s.kind === "self" ? "their own devices" : s.label || s.principal)).join(", ")}</b> as <b>{describeLogins(d.sshUsers)}</b>
                {d.sshCheckPeriod ? `, signing in again every ${checkPeriods.find((c) => c.value === d.sshCheckPeriod)?.label}` : ""}. Servers need SSH turned on (tailscale set --ssh).
              </>
            ) : (
              "Choose who, which servers, and which accounts."
            )}
          </div>
        </>
      ) : (
      <Field label="On">
        <div className="port-presets">
          {presets.map((p) => (
            <button
              key={p.label}
              type="button"
              className={!custom && preset === p ? "active" : ""}
              onClick={() => {
                setCustom(false);
                setD({ ...d, ports: p.ports, protocol: p.protocol });
              }}
            >
              {p.label}
            </button>
          ))}
          <button type="button" className={custom ? "active" : ""} onClick={() => setCustom(true)}>
            Custom…
          </button>
        </div>
        {custom && (
          <div className="inline-form mt">
            <Segmented
              value={d.protocol || "any"}
              options={[
                { value: "any", label: "Any" },
                { value: "tcp", label: "TCP" },
                { value: "udp", label: "UDP" },
              ]}
              onChange={(v) => setD({ ...d, protocol: v === "any" ? "" : (v as AccessRule["protocol"]) })}
            />
            <input value={d.ports === "*" ? "" : d.ports} placeholder="Ports, e.g. 22, 443, 8000-8100 (empty = all)" onChange={(e) => setD({ ...d, ports: e.target.value.trim() || "*" })} />
          </div>
        )}
        <div className="field-hint">
          {d.sources.length && d.destinations.length ? (
            <>
              <b>{d.sources.map((s) => s.label).join(", ")}</b> can connect to <b>{d.destinations.map((s) => s.label).join(", ")}</b> on {describePorts(d.protocol, d.ports)}.
            </>
          ) : (
            "Choose who the rule is for and what they can reach."
          )}
        </div>
      </Field>
      )}
    </Modal>
  );
}

function RuleRow({ r, canEdit, onEdit, onToggle, onDelete }: { r: AccessRule; canEdit: boolean; onEdit: () => void; onToggle: () => void; onDelete: () => void }) {
  return (
    <div className={`rule ${r.enabled ? "" : "is-paused"}`}>
      <div>
        <div className="rule-name">
          {r.name}
          {r.kind === "ssh" && <Badge tone="blue">SSH</Badge>}
          {!r.enabled && <Badge tone="amber">Paused</Badge>}
        </div>
        <div className="rule-flow">
          <PrincipalList items={r.sources} max={4} />
          <span className="rule-arrow">
            <Icon name={r.kind === "ssh" ? "terminal" : "arrow"} />
            {r.kind === "ssh" ? `SSH as ${describeLogins(r.sshUsers)}` : describePorts(r.protocol, r.ports)}
          </span>
          <PrincipalList items={r.destinations} max={4} />
          {r.kind === "ssh" && r.sshCheckPeriod && <Badge tone="violet">SSO every {checkPeriods.find((c) => c.value === r.sshCheckPeriod)?.label ?? r.sshCheckPeriod}</Badge>}
        </div>
        <div className="muted small mt-xs">
          Changed {timeAgo(r.updatedAt)}
          {r.updatedBy && ` by ${r.updatedBy}`}
        </div>
      </div>
      {canEdit && (
        <div className="rule-actions">
          <Button variant="ghost" onClick={onToggle} title={r.enabled ? "Pause" : "Resume"}>
            {r.enabled ? "Pause" : "Resume"}
          </Button>
          <Button onClick={onEdit}>Edit</Button>
          <button className="icon-btn" aria-label="Delete rule" onClick={onDelete}>
            <Icon name="trash" className="" />
          </button>
        </div>
      )}
    </div>
  );
}

export default function AccessPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const rules = useAsync(() => get<AccessRule[]>("/admin/api/rules"));
  const overview = useAsync(() => get<Overview>("/admin/api/overview"));
  const [editing, setEditing] = useState<Draft | null>(null);
  const [deleting, setDeleting] = useState<AccessRule | null>(null);
  const [busy, setBusy] = useState(false);

  if ((rules.loading && !rules.data) || (overview.loading && !overview.data)) return <Spinner />;
  if (rules.error) return <ErrorBox error={rules.error} onRetry={rules.reload} />;
  if (overview.error) return <ErrorBox error={overview.error} onRetry={overview.reload} />;

  const restricted = !!overview.data?.deviceRestriction;
  const list = rules.data ?? [];

  const setRestricted = async (enabled: boolean) => {
    setBusy(true);
    try {
      await put("/admin/api/settings/device-restriction", { enabled });
      toast.ok(enabled ? "Access is now limited to the rules below" : "Network opened to everyone");
      await overview.reload();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const toggleRule = async (r: AccessRule) => {
    try {
      await put(`/admin/api/rules/${r.id}`, ruleBody({ ...r, enabled: !r.enabled }));
      toast.ok(r.enabled ? "Rule paused" : "Rule resumed");
      void rules.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const blank: Draft = { kind: "network", name: "", enabled: true, sources: [], destinations: [], protocol: "", ports: "*", sshUsers: [], sshCheckPeriod: "" };

  return (
    <>
      <PageHeader
        title="Access"
        description="Who can reach what on your network. Changes reach every device within seconds."
        actions={
          canEdit && (
            <>
              <Button onClick={() => setEditing({ ...blank, kind: "ssh", sshUsers: ["ubuntu"] })}>
                <Icon name="terminal" /> New SSH rule
              </Button>
              <Button variant="primary" onClick={() => setEditing(blank)}>
                <Icon name="plus" /> New rule
              </Button>
            </>
          )
        }
      />

      <div className="card mode-card">
        <div className={`mode-icon ${restricted ? "is-locked" : "is-open"}`}>
          <Icon name={restricted ? "lock" : "unlock"} />
        </div>
        <div className="mode-title">
          <h2>Access control</h2>
          <div className={`mode-state ${restricted ? "is-on" : ""}`}>{restricted ? "On · only what the rules allow" : "Off · open network"}</div>
        </div>
        <div className="mode-switch">
          <Toggle checked={restricted} disabled={!canEdit || busy} onChange={setRestricted} label="Access control" hideLabel />
        </div>
        <p className="mode-desc muted">
          {restricted
            ? "People reach their own devices, the devices and servers given to them, and what the rules below allow. Nothing else is visible to them."
            : "Everyone can see and reach every device. Turn access control on to enforce the rules below."}
        </p>
      </div>

      {overview.data?.warnings?.map((w) => (
        <div key={w} className="callout callout-warn">
          {w}
        </div>
      ))}

      <div className="group-title">Always on</div>
      <div className="group">
        <div className="rule rule-builtin">
          <div>
            <div className="rule-name">Everyone reaches their own devices</div>
            <div className="rule-flow">
              <PrincipalList items={[{ kind: "everyone", principal: "" }]} />
              <span className="rule-arrow">
                <Icon name="arrow" /> All ports
              </span>
              <span className="chip chip-static">their own devices</span>
            </div>
          </div>
        </div>
        <div className="rule rule-builtin">
          <div>
            <div className="rule-name">Device and server assignments</div>
            <div className="muted">
              {overview.data?.assignedDevices ?? 0} device{overview.data?.assignedDevices === 1 ? "" : "s"} shared from the <Link to="/devices">Devices</Link> page, plus each server tag's access list on <Link to="/servers">Servers</Link>.
            </div>
          </div>
        </div>
      </div>

      <div className="group-title">Rules</div>
      {list.length === 0 ? (
        <Empty title="No rules yet">
          Add a rule to let a group reach servers, for example “Engineering can reach tag:staging on SSH”.
          {canEdit && (
            <div>
              <Button variant="primary" onClick={() => setEditing(blank)}>
                <Icon name="plus" /> New rule
              </Button>
            </div>
          )}
        </Empty>
      ) : (
        <div className="group">
          {list.map((r) => (
            <RuleRow
              key={r.id}
              r={r}
              canEdit={canEdit}
              onEdit={() => setEditing({ id: r.id, kind: r.kind, name: r.name, enabled: r.enabled, sources: r.sources, destinations: r.destinations, protocol: r.protocol, ports: r.ports, sshUsers: r.sshUsers ?? [], sshCheckPeriod: r.sshCheckPeriod ?? "" })}
              onToggle={() => void toggleRule(r)}
              onDelete={() => setDeleting(r)}
            />
          ))}
        </div>
      )}

      <details className="disclosure">
        <summary>Advanced: policy file</summary>
        <p className="muted mt">
          For experts only. Everything above is managed for you; you never need to edit this. Rules written here in Tailscale's policy format are applied in addition to the rules above.
        </p>
        <PolicyEditor />
      </details>

      {editing && <RuleEditor initial={editing} onClose={() => setEditing(null)} onSaved={() => void rules.reload()} />}
      {deleting && (
        <Confirm
          title={`Delete “${deleting.name}”?`}
          confirmLabel="Delete rule"
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await del(`/admin/api/rules/${deleting.id}`);
            toast.ok("Rule deleted");
            void rules.reload();
          }}
        >
          People who only had access through this rule lose it within seconds.
        </Confirm>
      )}
    </>
  );
}
