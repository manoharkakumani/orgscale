import { useState } from "react";
import { Link } from "react-router-dom";
import { del, get, post, put, type AccessRule, type Overview, type Principal } from "../api";
import { useCanEdit } from "../App";
import { PrincipalList, PrincipalPicker } from "../components/principals";
import { Badge, Button, Confirm, Empty, ErrorBox, Field, Icon, Modal, PageHeader, Segmented, Spinner, Toggle, timeAgo, useAsync, useToast } from "../components/ui";
import { PolicyEditor } from "./Policy";

const ruleKinds = ["everyone", "directory_group", "policy_group", "user", "tag", "device"] as const;

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

type Draft = { id?: number; name: string; enabled: boolean; sources: Principal[]; destinations: Principal[]; protocol: AccessRule["protocol"]; ports: string };

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
      const body = { ...d, name: d.name.trim() || autoName(), sources: d.sources.map(({ kind, principal }) => ({ kind, principal })), destinations: d.destinations.map(({ kind, principal }) => ({ kind, principal })) };
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
      title={d.id ? "Edit rule" : "New access rule"}
      onClose={onClose}
      xl
      footer={
        <>
          <Toggle checked={d.enabled} onChange={(enabled) => setD({ ...d, enabled })} label={d.enabled ? "Active" : "Paused"} />
          <span className="grow" />
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="primary" busy={busy} disabled={!d.sources.length || !d.destinations.length} onClick={save}>
            {d.id ? "Save rule" : "Add rule"}
          </Button>
        </>
      }
    >
      <Field label="Name">
        <input value={d.name} placeholder={autoName() || "e.g. Engineering can reach staging servers"} onChange={(e) => setD({ ...d, name: e.target.value })} autoFocus />
      </Field>

      <div className="builder">
        <div className="builder-col">
          <div className="field-label">Who</div>
          <PrincipalPicker kinds={[...ruleKinds]} value={d.sources} onChange={(sources) => setD({ ...d, sources })} height={220} />
        </div>
        <div className="builder-arrow">
          <Icon name="arrow" />
        </div>
        <div className="builder-col">
          <div className="field-label">Can reach</div>
          <PrincipalPicker kinds={[...ruleKinds]} value={d.destinations} onChange={(destinations) => setD({ ...d, destinations })} height={220} />
        </div>
      </div>

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
    </Modal>
  );
}

function RuleRow({ r, canEdit, onEdit, onToggle, onDelete }: { r: AccessRule; canEdit: boolean; onEdit: () => void; onToggle: () => void; onDelete: () => void }) {
  return (
    <div className={`rule ${r.enabled ? "" : "is-paused"}`}>
      <div>
        <div className="rule-name">
          {r.name}
          {!r.enabled && <Badge tone="amber">Paused</Badge>}
        </div>
        <div className="rule-flow">
          <PrincipalList items={r.sources} max={4} />
          <span className="rule-arrow">
            <Icon name="arrow" />
            {describePorts(r.protocol, r.ports)}
          </span>
          <PrincipalList items={r.destinations} max={4} />
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
      await put(`/admin/api/rules/${r.id}`, { name: r.name, protocol: r.protocol, ports: r.ports, enabled: !r.enabled, sources: r.sources.map(({ kind, principal }) => ({ kind, principal })), destinations: r.destinations.map(({ kind, principal }) => ({ kind, principal })) });
      toast.ok(r.enabled ? "Rule paused" : "Rule resumed");
      void rules.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const blank: Draft = { name: "", enabled: true, sources: [], destinations: [], protocol: "", ports: "*" };

  return (
    <>
      <PageHeader
        title="Access"
        description="Who can reach what on your network. Changes reach every device within seconds."
        actions={
          canEdit && (
            <Button variant="primary" onClick={() => setEditing(blank)}>
              <Icon name="plus" /> New rule
            </Button>
          )
        }
      />

      <div className="card mode-card">
        <div className={`mode-icon ${restricted ? "is-locked" : "is-open"}`}>
          <Icon name={restricted ? "lock" : "unlock"} />
        </div>
        <div className="grow">
          <h2 style={{ margin: 0 }}>{restricted ? "Only what the rules allow" : "Open network"}</h2>
          <div className="muted">
            {restricted
              ? "People reach their own devices, the devices and servers assigned to them, and whatever the rules below allow. Nothing else is visible."
              : "Everyone can see and reach every device. Turn on access control to enforce the rules below."}
          </div>
        </div>
        <Toggle checked={restricted} disabled={!canEdit || busy} onChange={setRestricted} label="Access control" />
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
              onEdit={() => setEditing({ id: r.id, name: r.name, enabled: r.enabled, sources: r.sources, destinations: r.destinations, protocol: r.protocol, ports: r.ports })}
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
