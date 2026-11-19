import { useEffect, useMemo, useState } from "react";
import { get, post, type Principal, type ServerTag, type TempAccess } from "../api";
import { useCanEdit } from "../App";
import { PrincipalPicker } from "../components/principals";
import { Badge, Button, Confirm, Empty, ErrorBox, Field, Icon, Modal, PageHeader, Segmented, Spinner, Toggle, timeAgo, useAsync, useToast } from "../components/ui";

export const durationChoices = [
  { minutes: 15, label: "15 minutes" },
  { minutes: 30, label: "30 minutes" },
  { minutes: 60, label: "1 hour" },
  { minutes: 120, label: "2 hours" },
  { minutes: 240, label: "4 hours" },
  { minutes: 480, label: "8 hours" },
  { minutes: 720, label: "12 hours" },
  { minutes: 1440, label: "1 day" },
  { minutes: 4320, label: "3 days" },
  { minutes: 10080, label: "7 days" },
];

export function duration(minutes: number): string {
  const d = durationChoices.find((c) => c.minutes === minutes);
  if (d) return d.label;
  if (minutes % 1440 === 0) return `${minutes / 1440} days`;
  if (minutes % 60 === 0) return `${minutes / 60} hours`;
  return `${minutes} minutes`;
}

/** timeLeft says how long until iso, e.g. "1 h 20 min left". */
function timeLeft(iso?: string, now = Date.now()): string {
  if (!iso) return "";
  const ms = new Date(iso).getTime() - now;
  if (ms <= 0) return "ending";
  const m = Math.floor(ms / 60000);
  if (m < 1) return "under a minute left";
  if (m < 60) return `${m} min left`;
  const h = Math.floor(m / 60);
  if (h < 48) return `${h} h ${m % 60 ? `${m % 60} min ` : ""}left`;
  return `${Math.floor(h / 24)} days left`;
}

// Re-render every 30 seconds so countdowns stay current.
function useTick() {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 30000);
    return () => window.clearInterval(id);
  }, []);
  return now;
}

function GrantForm({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const tags = useAsync(() => get<ServerTag[]>("/admin/api/tags"));
  const managed = (tags.data ?? []).filter((t) => t.managed);
  const [to, setTo] = useState<Principal[]>([]);
  const [tag, setTag] = useState("");
  const [scope, setScope] = useState<"tag" | "server">("tag");
  const [server, setServer] = useState(0);
  const [minutes, setMinutes] = useState(60);
  const [ssh, setSSH] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const chosen = managed.find((t) => t.name === (tag || managed[0]?.name));
  // Every server (tagged device), once, with the tags it carries.
  const servers = useMemo(() => {
    const byID = new Map<number, { id: number; name: string; tags: string[] }>();
    for (const t of tags.data ?? []) {
      for (const d of t.devices) {
        const s = byID.get(d.id) ?? { id: d.id, name: d.name, tags: [] };
        s.tags.push(t.name);
        byID.set(d.id, s);
      }
    }
    return [...byID.values()].sort((a, b) => a.name.localeCompare(b.name));
  }, [tags.data]);
  const serverID = server || servers[0]?.id || 0;
  const sshUsers = scope === "tag" ? (chosen?.tempSSHUsers ?? []) : [];
  const ready = scope === "tag" ? !!chosen : serverID > 0;

  return (
    <Modal
      title="Grant temporary access"
      onClose={onClose}
      wide
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={busy}
            disabled={!to.length || !ready}
            onClick={async () => {
              setBusy(true);
              try {
                await post("/admin/api/temp-access", {
                  to: to.map(({ kind, principal }) => ({ kind, principal })),
                  ...(scope === "tag" ? { tag: chosen!.name } : { server: serverID }),
                  minutes,
                  reason: reason.trim(),
                  ssh: ssh && sshUsers.length > 0,
                });
                toast.ok(`Access granted for ${duration(minutes)}`);
                onDone();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Grant access
          </Button>
        </>
      }
    >
      <Field label="Who" hint="People, or groups: a group's access covers whoever is in it while it lasts.">
        <PrincipalPicker kinds={["directory_group", "user"]} value={to} onChange={setTo} placeholder="Search people and groups" height={220} />
      </Field>
      <Field label="To">
        <Segmented
          value={scope}
          onChange={setScope}
          options={[
            { value: "tag", label: "A whole tag" },
            { value: "server", label: "One server" },
          ]}
        />
      </Field>
      <div className="grid-2">
        {scope === "tag" ? (
          <Field label="Tag" hint="Every server with this tag, including ones added during the grant.">
            {tags.loading && !tags.data ? (
              <Spinner />
            ) : managed.length ? (
              <select value={chosen?.name ?? ""} onChange={(e) => setTag(e.target.value)}>
                {managed.map((t) => (
                  <option key={t.name} value={t.name}>
                    {t.name}
                    {t.description ? ` — ${t.description}` : ""}
                  </option>
                ))}
              </select>
            ) : (
              <span className="muted">Create a server tag on the Servers page first.</span>
            )}
          </Field>
        ) : (
          <Field label="Server" hint="Just this machine, on every port.">
            {tags.loading && !tags.data ? (
              <Spinner />
            ) : servers.length ? (
              <select value={serverID} onChange={(e) => setServer(Number(e.target.value))}>
                {servers.map((d) => (
                  <option key={d.id} value={d.id}>
                    {d.name} ({d.tags.join(", ")})
                  </option>
                ))}
              </select>
            ) : (
              <span className="muted">No servers have joined yet.</span>
            )}
          </Field>
        )}
        <Field label="For how long" hint="Access ends on its own.">
          <select value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
            {durationChoices.map((d) => (
              <option key={d.minutes} value={d.minutes}>
                {d.label}
              </option>
            ))}
          </select>
        </Field>
      </div>
      <Field label="Reason (optional)" hint="Kept in the audit log.">
        <input value={reason} maxLength={500} onChange={(e) => setReason(e.target.value)} placeholder="e.g. Incident INC-311" />
      </Field>
      <div className="group">
        <div className="prow">
          <div className="prow-label">
            <div>Also SSH in</div>
            <div className="muted small">
              {scope === "server" ? (
                <>Keyless SSH is given per tag. To SSH into just this server without keys, give it its own tag. Their own SSH keys still work.</>
              ) : sshUsers.length ? (
                <>As {sshUsers.join(" or ")}. No keys needed.</>
              ) : (
                <>Set SSH accounts on this tag first: Servers → Edit access.</>
              )}
            </div>
          </div>
          <div className="prow-control">
            <Toggle hideLabel label="Also SSH in" checked={ssh && sshUsers.length > 0} disabled={!sshUsers.length} onChange={setSSH} />
          </div>
        </div>
      </div>
    </Modal>
  );
}

function GrantRow({ g, now, onEnd }: { g: TempAccess; now: number; onEnd?: () => void }) {
  return (
    <div className="request card">
      <div className="request-head">
        <div className="cell-stack grow">
          <span>
            <Icon name={g.kind === "directory_group" ? "users" : "user"} className="inline-icon" /> <b>{g.grantee}</b> → {g.server ? <><Icon name="server" className="inline-icon" /> {g.tag}</> : <code>{g.tag}</code>} for {duration(g.minutes)}
            {g.ssh && g.sshUsers.length ? <> · SSH as {g.sshUsers.join(", ")}</> : null}
          </span>
          <span className="muted small">
            Granted by {g.grantedBy} {timeAgo(g.grantedAt)}
            {g.status === "ended" && g.endedBy ? ` · ended early by ${g.endedBy}` : ""}
          </span>
        </div>
        {g.active ? <Badge tone="green">Active · {timeLeft(g.expiresAt, now)}</Badge> : <Badge>{g.status === "ended" ? "Ended early" : "Ended"}</Badge>}
      </div>
      {g.reason && <p className="request-reason">{g.reason}</p>}
      {onEnd && (
        <div className="request-actions">
          <Button variant="danger" onClick={onEnd}>
            End now
          </Button>
        </div>
      )}
    </div>
  );
}

type Filter = "active" | "history";

export default function TempAccessPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const now = useTick();
  const { data, error, loading, reload } = useAsync(() => get<TempAccess[]>("/admin/api/temp-access"));
  const [filter, setFilter] = useState<Filter>("active");
  const [granting, setGranting] = useState(false);
  const [ending, setEnding] = useState<TempAccess | null>(null);

  // Grants lapse on the server; refresh so they move to history.
  useEffect(() => {
    const id = window.setInterval(() => void reload(), 30000);
    return () => window.clearInterval(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const active = useMemo(() => (data ?? []).filter((g) => g.active), [data]);
  const past = useMemo(() => (data ?? []).filter((g) => !g.active), [data]);
  const rows = filter === "active" ? active : past;

  return (
    <>
      <PageHeader
        title="Temporary access"
        description="Give people or groups access to one server or a whole tag for a set time, for an incident or a one-off task. It ends on its own, and every grant is in the audit log."
        actions={
          <>
            <Segmented value={filter} onChange={setFilter} options={[{ value: "active", label: `Active (${active.length})` }, { value: "history", label: "History" }]} />
            {canEdit && (
              <Button variant="primary" onClick={() => setGranting(true)}>
                <Icon name="plus" /> Grant access
              </Button>
            )}
          </>
        }
      />
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty title={filter === "active" ? "No temporary access right now" : "No past grants"}>{filter === "active" && canEdit ? "Click Grant access to give someone access for a while." : null}</Empty>
      ) : (
        <div className="request-list">
          {rows.map((g) => (
            <GrantRow key={g.id} g={g} now={now} onEnd={canEdit && g.active ? () => setEnding(g) : undefined} />
          ))}
        </div>
      )}
      {granting && <GrantForm onClose={() => setGranting(false)} onDone={() => void reload()} />}
      {ending && (
        <Confirm
          title="End this access now?"
          confirmLabel="End now"
          danger
          onClose={() => setEnding(null)}
          onConfirm={async () => {
            await post(`/admin/api/temp-access/${ending.id}/end`);
            toast.ok("Access ended");
            void reload();
          }}
        >
          {ending.grantee} loses access to {ending.tag} straight away.
        </Confirm>
      )}
    </>
  );
}
