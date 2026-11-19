import { useEffect, useMemo, useState } from "react";
import { get, post, type AccessRequest, type Principal, type RequestTarget, type ServerTag } from "../api";
import { PrincipalPicker } from "../components/principals";
import { useCanEdit, useMe } from "../App";
import { Badge, Button, Empty, ErrorBox, Field, Icon, Modal, PageHeader, Segmented, Spinner, Toggle, timeAgo, useAsync, useToast } from "../components/ui";

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

function StatusBadge({ r, now }: { r: AccessRequest; now: number }) {
  if (r.active) return <Badge tone="green">Active · {timeLeft(r.expiresAt, now)}</Badge>;
  const tone = { pending: "amber", approved: "neutral", denied: "red", cancelled: "neutral", revoked: "neutral", expired: "neutral" } as const;
  const label = { pending: "Waiting for approval", approved: "Ended", denied: "Denied", cancelled: "Withdrawn", revoked: "Ended early", expired: r.decidedAt ? "Ended" : "Not decided in time" };
  return <Badge tone={tone[r.status]}>{label[r.status]}</Badge>;
}

function RequestForm({ targets, onClose, onSent }: { targets: RequestTarget[]; onClose: () => void; onSent: () => void }) {
  const toast = useToast();
  const [tag, setTag] = useState(targets[0]?.tag ?? "");
  const target = targets.find((t) => t.tag === tag);
  const choices = durationChoices.filter((d) => !target || d.minutes <= target.maxMinutes);
  const [minutes, setMinutes] = useState(60);
  const [reason, setReason] = useState("");
  const [ssh, setSSH] = useState(false);
  const [busy, setBusy] = useState(false);
  const usable = choices.some((c) => c.minutes === minutes) ? minutes : (choices[choices.length - 1]?.minutes ?? 60);

  return (
    <Modal
      title="Request access"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={busy}
            disabled={!tag || reason.trim().length < 5}
            onClick={async () => {
              setBusy(true);
              try {
                await post("/admin/api/my/requests", { tag, minutes: usable, reason: reason.trim(), ssh: ssh && !!target?.sshUsers.length });
                toast.ok("Request sent. An administrator will review it.");
                onSent();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Send request
          </Button>
        </>
      }
    >
      <Field label="Servers" hint="Only the servers an administrator opened to you are listed.">
        <select value={tag} onChange={(e) => setTag(e.target.value)}>
          {targets.map((t) => (
            <option key={t.tag} value={t.tag}>
              {t.tag}
              {t.description ? ` — ${t.description}` : ""}
            </option>
          ))}
        </select>
      </Field>
      <Field label="For how long" hint={target ? `Up to ${duration(target.maxMinutes)}. Access ends on its own.` : undefined}>
        <select value={usable} onChange={(e) => setMinutes(Number(e.target.value))}>
          {choices.map((d) => (
            <option key={d.minutes} value={d.minutes}>
              {d.label}
            </option>
          ))}
        </select>
      </Field>
      <Field label="Why do you need it?" hint="Approvers see this, and it is kept in the audit log.">
        <textarea rows={3} value={reason} maxLength={500} placeholder="e.g. Investigating ticket OPS-142: replication lag on the primary" onChange={(e) => setReason(e.target.value)} />
      </Field>
      {!!target?.sshUsers.length && (
        <div className="group">
          <div className="prow">
            <div className="prow-label">
              <div>Also SSH in</div>
              <div className="muted small">
                As {target.sshUsers.join(" or ")}, with <code>ssh {target.sshUsers[0]}@server</code>. No keys needed.
              </div>
            </div>
            <div className="prow-control">
              <Toggle hideLabel label="Also SSH in" checked={ssh} onChange={setSSH} />
            </div>
          </div>
        </div>
      )}
    </Modal>
  );
}

function RequestRow({ r, now, children, showWho }: { r: AccessRequest; now: number; children?: React.ReactNode; showWho?: boolean }) {
  return (
    <div className="request card">
      <div className="request-head">
        <div className="cell-stack grow">
          <span>
            {r.granted && <Badge tone="violet">Granted</Badge>} {showWho && <b>{r.requester} </b>}
            {showWho ? (r.active ? "has " : r.status === "pending" ? "wants " : r.granted ? "had " : "asked for ") : ""}
            <code>{r.tag}</code> for {duration(r.minutes)}
            {r.ssh && r.sshUsers?.length ? <> · SSH as {r.sshUsers.join(", ")}</> : null}
          </span>
          <span className="muted small">{r.granted ? `Granted ${timeAgo(r.createdAt)}` : `Asked ${timeAgo(r.createdAt)}`}</span>
        </div>
        <StatusBadge r={r} now={now} />
      </div>
      {!(r.granted && r.reason === "granted by an administrator") && <p className="request-reason">“{r.reason}”</p>}
      {r.decidedBy && r.status !== "pending" && (
        <p className="muted small">
          {r.status === "denied" ? "Denied" : r.status === "cancelled" ? "Withdrawn" : r.status === "revoked" ? "Ended" : r.granted ? "Granted" : "Approved"} by {r.decidedBy} {timeAgo(r.decidedAt)}
          {r.decisionNote ? ` — ${r.decisionNote}` : ""}
        </p>
      )}
      {children && <div className="request-actions">{children}</div>}
    </div>
  );
}

/** MyAccessPage is what people who are not administrators see. */
export function MyAccessPage() {
  const me = useMe();
  const toast = useToast();
  const now = useTick();
  const targets = useAsync(() => get<RequestTarget[]>("/admin/api/my/targets"));
  const mine = useAsync(() => get<AccessRequest[]>("/admin/api/my/requests"));
  const [asking, setAsking] = useState(false);

  // Watch for the decision while something is waiting.
  useEffect(() => {
    if (!mine.data?.some((r) => r.status === "pending" || r.active)) return;
    const id = window.setInterval(() => void mine.reload(), 15000);
    return () => window.clearInterval(id);
  }, [mine.data]); // eslint-disable-line react-hooks/exhaustive-deps

  return (
    <>
      <PageHeader
        title="Request access"
        description={`Hi ${me.name || me.email}. Ask for temporary access to servers you don't normally reach. An administrator approves it, and it ends on its own.`}
        actions={
          !!targets.data?.length && (
            <Button variant="primary" onClick={() => setAsking(true)}>
              <Icon name="plus" /> Request access
            </Button>
          )
        }
      />
      {(targets.error || mine.error) && <ErrorBox error={targets.error || mine.error} onRetry={() => { void targets.reload(); void mine.reload(); }} />}
      {(targets.loading && !targets.data) || (mine.loading && !mine.data) ? (
        <Spinner />
      ) : !targets.data?.length && !mine.data?.length ? (
        <Empty title="Nothing to request yet">Your administrators haven't opened any servers for requests. Your own devices and the servers you've been given work already.</Empty>
      ) : (
        <>
          {!mine.data?.length ? (
            <Empty title="No requests yet">Click Request access when you need to reach a server.</Empty>
          ) : (
            <div className="request-list">
              {mine.data.map((r) => (
                <RequestRow key={r.id} r={r} now={now}>
                  {(r.status === "pending" || r.active) && (
                    <Button
                      onClick={async () => {
                        try {
                          await post(`/admin/api/my/requests/${r.id}/cancel`);
                          toast.ok(r.active ? "Access ended" : "Request withdrawn");
                          void mine.reload();
                        } catch (e) {
                          toast.error(e);
                        }
                      }}
                    >
                      {r.active ? "End access now" : "Withdraw"}
                    </Button>
                  )}
                </RequestRow>
              ))}
            </div>
          )}
        </>
      )}
      {asking && targets.data && <RequestForm targets={targets.data} onClose={() => setAsking(false)} onSent={() => void mine.reload()} />}
    </>
  );
}

function Decide({ r, kind, onClose, onDone }: { r: AccessRequest; kind: "approve" | "deny" | "revoke"; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [note, setNote] = useState("");
  const [minutes, setMinutes] = useState(r.minutes);
  const [busy, setBusy] = useState(false);
  const title = { approve: "Approve access", deny: "Deny request", revoke: "End access now" }[kind];

  return (
    <Modal
      title={title}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant={kind === "approve" ? "primary" : "danger"}
            busy={busy}
            onClick={async () => {
              setBusy(true);
              try {
                await post(`/admin/api/requests/${r.id}/${kind}`, { note, minutes: kind === "approve" ? minutes : 0 });
                toast.ok({ approve: "Approved. Access starts now.", deny: "Request denied", revoke: "Access ended" }[kind]);
                onDone();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            {title}
          </Button>
        </>
      }
    >
      <p>
        <b>{r.requester}</b> → <code>{r.tag}</code>
        {r.ssh && r.sshUsers?.length ? <> with SSH as {r.sshUsers.join(", ")}</> : null}
      </p>
      <p className="muted">“{r.reason}”</p>
      {kind === "approve" && (
        <Field label="For how long" hint="You can give less time than was asked for.">
          <select value={minutes} onChange={(e) => setMinutes(Number(e.target.value))}>
            {durationChoices
              .filter((d) => d.minutes <= r.minutes)
              .map((d) => (
                <option key={d.minutes} value={d.minutes}>
                  {d.label}
                </option>
              ))}
            {!durationChoices.some((d) => d.minutes === r.minutes) && <option value={r.minutes}>{duration(r.minutes)}</option>}
          </select>
        </Field>
      )}
      <Field label="Note (optional)" hint="The requester sees it; it goes in the audit log.">
        <input value={note} maxLength={500} onChange={(e) => setNote(e.target.value)} placeholder={kind === "deny" ? "e.g. Use the staging database instead" : ""} />
      </Field>
    </Modal>
  );
}

function GrantForm({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const tags = useAsync(() => get<ServerTag[]>("/admin/api/tags"));
  const managed = (tags.data ?? []).filter((t) => t.managed);
  const [to, setTo] = useState<Principal[]>([]);
  const [tag, setTag] = useState("");
  const [minutes, setMinutes] = useState(60);
  const [ssh, setSSH] = useState(false);
  const [reason, setReason] = useState("");
  const [busy, setBusy] = useState(false);
  const chosen = managed.find((t) => t.name === (tag || managed[0]?.name));

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
            disabled={!to.length || !chosen}
            onClick={async () => {
              setBusy(true);
              try {
                await post("/admin/api/requests/grant", {
                  to: to.map(({ kind, principal }) => ({ kind, principal })),
                  tag: chosen!.name,
                  minutes,
                  reason: reason.trim(),
                  ssh: ssh && !!chosen?.requestSSHUsers.length,
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
      <Field label="Who" hint="People, or groups: everyone in a group at the time gets access, including people added during it.">
        <PrincipalPicker kinds={["directory_group", "user"]} value={to} onChange={setTo} placeholder="Search people and groups" height={220} />
      </Field>
      <div className="grid-2">
        <Field label="Servers">
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
              {chosen?.requestSSHUsers.length ? (
                <>As {chosen.requestSSHUsers.join(" or ")}. No keys needed.</>
              ) : (
                <>Set SSH accounts on this tag first (Servers → edit tag → Temporary access).</>
              )}
            </div>
          </div>
          <div className="prow-control">
            <Toggle hideLabel label="Also SSH in" checked={ssh && !!chosen?.requestSSHUsers.length} disabled={!chosen?.requestSSHUsers.length} onChange={setSSH} />
          </div>
        </div>
      </div>
    </Modal>
  );
}

type Filter = "open" | "all";

/** RequestsPage is where administrators decide on access requests. */
export default function RequestsPage() {
  const canEdit = useCanEdit();
  const me = useMe();
  const now = useTick();
  const { data, error, loading, reload } = useAsync(() => get<AccessRequest[]>("/admin/api/requests"));
  // Keep the sidebar count in step with this page.
  useEffect(() => {
    if (data) window.dispatchEvent(new CustomEvent("orgscale:requests", { detail: data.filter((r) => r.status === "pending").length }));
  }, [data]);
  const [filter, setFilter] = useState<Filter>("open");
  const [deciding, setDeciding] = useState<{ r: AccessRequest; kind: "approve" | "deny" | "revoke" } | null>(null);
  const [granting, setGranting] = useState(false);

  useEffect(() => {
    const id = window.setInterval(() => void reload(), 20000);
    return () => window.clearInterval(id);
  }, []); // eslint-disable-line react-hooks/exhaustive-deps

  const pending = useMemo(() => (data ?? []).filter((r) => r.status === "pending"), [data]);
  const active = useMemo(() => (data ?? []).filter((r) => r.active), [data]);
  const rest = useMemo(() => (data ?? []).filter((r) => r.status !== "pending" && !r.active), [data]);

  const own = (r: AccessRequest) => r.requester.toLowerCase() === me.email.toLowerCase();

  return (
    <>
      <PageHeader
        title="Access requests"
        description="Temporary access to servers: people request it for tags that allow requests, or you grant it directly to people and groups. It ends on its own; every step is in the audit log."
        actions={
          <>
            <Segmented value={filter} onChange={setFilter} options={[{ value: "open", label: `Open (${pending.length + active.length})` }, { value: "all", label: "History" }]} />
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
      ) : filter === "open" ? (
        pending.length + active.length === 0 ? (
          <Empty title="Nothing waiting">
            New requests show up here. People request access by signing in at <code>/admin/</code> with single sign-on; you can also grant access directly with Grant access.
          </Empty>
        ) : (
          <>
            {pending.length > 0 && <div className="group-title">Waiting for a decision</div>}
            <div className="request-list">
              {pending.map((r) => (
                <RequestRow key={r.id} r={r} now={now} showWho>
                  {canEdit &&
                    (own(r) ? (
                      <span className="muted small">Your own request: another administrator has to decide.</span>
                    ) : (
                      <>
                        <Button onClick={() => setDeciding({ r, kind: "deny" })}>Deny</Button>
                        <Button variant="primary" onClick={() => setDeciding({ r, kind: "approve" })}>
                          Approve
                        </Button>
                      </>
                    ))}
                </RequestRow>
              ))}
            </div>
            {active.length > 0 && <div className="group-title">Active now</div>}
            <div className="request-list">
              {active.map((r) => (
                <RequestRow key={r.id} r={r} now={now} showWho>
                  {canEdit && (
                    <Button variant="danger" onClick={() => setDeciding({ r, kind: "revoke" })}>
                      End now
                    </Button>
                  )}
                </RequestRow>
              ))}
            </div>
          </>
        )
      ) : rest.length === 0 ? (
        <Empty title="No history yet" />
      ) : (
        <div className="request-list">
          {rest.map((r) => (
            <RequestRow key={r.id} r={r} now={now} showWho />
          ))}
        </div>
      )}
      {granting && <GrantForm onClose={() => setGranting(false)} onDone={() => void reload()} />}
      {deciding && <Decide r={deciding.r} kind={deciding.kind} onClose={() => setDeciding(null)} onDone={() => void reload()} />}
    </>
  );
}
