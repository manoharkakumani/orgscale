import { useMemo, useState } from "react";
import { del, get, post, type AdminUser } from "../api";
import { useCanEdit, useMe } from "../App";
import { MList, MRow } from "../components/mlist";
import { Badge, Button, Confirm, Empty, ErrorBox, Field, Group, Modal, PRow, PageHeader, SearchBox, Spinner, timeAgo, useAsync, useToast } from "../components/ui";

const providerLabel: Record<string, string> = {
  oidc: "SSO",
  scim: "SCIM",
  google: "Google",
  local: "Local",
  cli: "Local",
};

function AddUserModal({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const [name, setName] = useState("");
  const [email, setEmail] = useState("");
  const [busy, setBusy] = useState(false);
  const toast = useToast();
  return (
    <Modal
      title="Add a local user"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={busy}
            disabled={!name.trim()}
            onClick={async () => {
              setBusy(true);
              try {
                await post("/api/v1/user", { name: name.trim(), email: email.trim() || undefined });
                toast.ok(`Added ${name}`);
                onDone();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Add user
          </Button>
        </>
      }
    >
      <p className="muted">
        Most users don't need this: they're added automatically the first time they sign in with SSO, or by SCIM / Google sync. Local users are useful for service accounts.
      </p>
      <Field label="Username" hint="Letters, digits, '-', '.', '_' and at most one '@'.">
        <input value={name} onChange={(e) => setName(e.target.value)} autoFocus />
      </Field>
      <Field label="Email (optional)">
        <input value={email} onChange={(e) => setEmail(e.target.value)} type="email" />
      </Field>
    </Modal>
  );
}

export default function UsersPage() {
  const canEdit = useCanEdit();
  const me = useMe();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<AdminUser[]>("/admin/api/users"));
  const [q, setQ] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);
  const isSelf = (u: AdminUser) => !!u.email && u.email.toLowerCase() === me.email.toLowerCase();
  const [status, setStatus] = useState<"all" | "active" | "disabled">("all");
  const [confirm, setConfirm] = useState<{ kind: "disable" | "remove"; user: AdminUser } | null>(null);
  const [adding, setAdding] = useState(false);

  const open = data?.find((u) => u.id === openId);
  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data ?? [])
      .filter((u) => (status === "all" ? true : status === "disabled" ? u.disabled : !u.disabled))
      .filter((u) => !needle || [u.name, u.displayName, u.email, ...u.groups].some((s) => s?.toLowerCase().includes(needle)))
      .sort((a, b) => (a.displayName || a.email || a.name).localeCompare(b.displayName || b.email || b.name));
  }, [data, q, status]);

  const enable = async (u: AdminUser) => {
    try {
      await post(`/admin/api/users/${u.id}/enable`);
      toast.ok(`${u.email || u.name} can sign in again`);
      void reload();
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <>
      <PageHeader
        title="Users"
        description="People in your organization. Users appear when they first sign in with SSO, or when SCIM / Google sync provisions them."
        actions={canEdit && <Button onClick={() => setAdding(true)}>Add local user</Button>}
      />

      <div className="toolbar">
        <SearchBox value={q} onChange={setQ} placeholder="Search name, email or group" />
        <div className="segmented">
          {(["all", "active", "disabled"] as const).map((s) => (
            <button key={s} className={status === s ? "active" : ""} onClick={() => setStatus(s)}>
              {s[0].toUpperCase() + s.slice(1)}
            </button>
          ))}
        </div>
      </div>

      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty title={q ? "No users match" : "No users yet"}>Users appear here after they sign in with SSO or are provisioned.</Empty>
      ) : (
        <>
        <MList>
          {rows.map((u) => (
            <MRow
              key={u.id}
              muted={u.disabled}
              onClick={() => setOpenId(u.id)}
              lead={u.picture ? <img className="avatar" src={u.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-fallback">{(u.displayName || u.email || u.name).slice(0, 1).toUpperCase()}</span>}
              title={u.displayName || u.name}
              sub={[u.email || u.name, `${u.devices} device${u.devices === 1 ? "" : "s"}`].join(" · ")}
              trail={u.disabled ? <Badge tone="red">Disabled</Badge> : undefined}
            />
          ))}
        </MList>
        <div className="table-wrap card desktop-only">
          <table>
            <thead>
              <tr>
                <th>User</th>
                <th>Source</th>
                <th>Groups</th>
                <th className="num">Devices</th>
                <th>Last seen</th>
                <th>Status</th>
                {canEdit && <th aria-label="Actions" />}
              </tr>
            </thead>
            <tbody>
              {rows.map((u) => {
                const self = u.email && u.email.toLowerCase() === me.email.toLowerCase();
                return (
                  <tr key={u.id} className={u.disabled ? "row-muted" : ""}>
                    <td>
                      <div className="cell-user">
                        {u.picture ? <img className="avatar avatar-sm" src={u.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-sm avatar-fallback">{(u.displayName || u.email || u.name).slice(0, 1).toUpperCase()}</span>}
                        <div>
                          <div>{u.displayName || u.name}</div>
                          <div className="muted small">{u.email || u.name}</div>
                        </div>
                      </div>
                    </td>
                    <td>
                      <Badge>{providerLabel[u.provider] ?? u.provider}</Badge>
                    </td>
                    <td>
                      <div className="chip-row">
                        {u.groups.length ? u.groups.map((g) => <span key={g} className="chip chip-static">{g}</span>) : <span className="muted">—</span>}
                      </div>
                    </td>
                    <td className="num">{u.devices}</td>
                    <td className="muted">{timeAgo(u.lastSeen)}</td>
                    <td>{u.disabled ? <Badge tone="red" title={`by ${u.disabledBy ?? "?"}`}>Disabled</Badge> : <Badge tone="green">Active</Badge>}</td>
                    {canEdit && (
                      <td className="actions">
                        {u.disabled ? (
                          <Button variant="ghost" onClick={() => enable(u)}>
                            Enable
                          </Button>
                        ) : (
                          <Button variant="ghost" disabled={!!self} title={self ? "You cannot disable yourself" : ""} onClick={() => setConfirm({ kind: "disable", user: u })}>
                            Disable
                          </Button>
                        )}
                        <Button variant="ghost" className="text-danger" disabled={!!self} onClick={() => setConfirm({ kind: "remove", user: u })}>
                          Remove
                        </Button>
                      </td>
                    )}
                  </tr>
                );
              })}
            </tbody>
          </table>
        </div>
        </>
      )}

      {open && (
        <Modal title={open.displayName || open.name} onClose={() => setOpenId(null)}>
          <div className="cell-user" style={{ marginBottom: 14 }}>
            {open.picture ? <img className="avatar" src={open.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-fallback">{(open.displayName || open.email || open.name).slice(0, 1).toUpperCase()}</span>}
            <div className="cell-stack">
              <span>{open.email || open.name}</span>
              <span className="muted small">{providerLabel[open.provider] ?? open.provider}</span>
            </div>
          </div>
          <Group>
            <PRow label="Status">{open.disabled ? <Badge tone="red">Disabled</Badge> : <Badge tone="green">Active</Badge>}</PRow>
            <PRow label="Devices">{open.devices}</PRow>
            <PRow label="Last seen">{timeAgo(open.lastSeen)}</PRow>
            <PRow label="Groups" block>
              <div className="chip-row">{open.groups.length ? open.groups.map((g) => <span key={g} className="chip chip-static">{g}</span>) : <span className="muted">None</span>}</div>
            </PRow>
          </Group>
          {canEdit && (
            <div className="sheet-actions">
              {open.disabled ? (
                <Button onClick={() => { setOpenId(null); void enable(open); }}>Enable</Button>
              ) : (
                <Button disabled={isSelf(open)} onClick={() => { setOpenId(null); setConfirm({ kind: "disable", user: open }); }}>
                  Disable
                </Button>
              )}
              <Button variant="danger" disabled={isSelf(open)} onClick={() => { setOpenId(null); setConfirm({ kind: "remove", user: open }); }}>
                Remove from organization
              </Button>
              {isSelf(open) && <p className="muted small">You can't disable or remove yourself.</p>}
            </div>
          )}
        </Modal>
      )}

      {confirm?.kind === "disable" && (
        <Confirm
          title={`Disable ${confirm.user.email || confirm.user.name}?`}
          confirmLabel="Disable user"
          danger
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            await post(`/admin/api/users/${confirm.user.id}/disable`);
            toast.ok("User disabled");
            void reload();
          }}
        >
          <p>This takes effect immediately:</p>
          <ul>
            <li>their {confirm.user.devices} device{confirm.user.devices === 1 ? " is" : "s are"} removed from the network,</li>
            <li>their personal auth keys are expired,</li>
            <li>they can no longer sign in.</li>
          </ul>
          <p className="muted">The user record and group memberships are kept so you can enable them again.</p>
        </Confirm>
      )}

      {confirm?.kind === "remove" && (
        <Confirm
          title={`Remove ${confirm.user.email || confirm.user.name} from the organization?`}
          confirmLabel="Remove user"
          danger
          onClose={() => setConfirm(null)}
          onConfirm={async () => {
            await del(`/admin/api/users/${confirm.user.id}`);
            toast.ok("User removed");
            void reload();
          }}
        >
          <p>This deletes the user, their {confirm.user.devices} device{confirm.user.devices === 1 ? "" : "s"}, their personal auth keys and every device access granted to them. It cannot be undone.</p>
          <p className="muted">If your identity provider still allows them, they could sign in again as a new user. Remove them there too, or use Disable instead.</p>
        </Confirm>
      )}

      {adding && <AddUserModal onClose={() => setAdding(false)} onDone={reload} />}
    </>
  );
}
