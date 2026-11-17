import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { del, get, post, put, type AdminDevice, type AdminGroup, type AdminUser, type DeviceGrant } from "../api";
import { useCanEdit } from "../App";
import { Badge, Button, ChipInput, Confirm, Empty, ErrorBox, Modal, PageHeader, SearchBox, Spinner, dateTime, timeAgo, useAsync, useToast } from "../components/ui";

function Status({ d }: { d: AdminDevice }) {
  if (d.expired) return <Badge tone="red">Expired</Badge>;
  return d.online ? <Badge tone="green">Online</Badge> : <Badge>Offline</Badge>;
}

type Option = { key: string; kind: DeviceGrant["kind"]; principal: string; label: string; sub: string };

function AccessEditor({ device, onSaved }: { device: AdminDevice; onSaved: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const users = useAsync(() => get<AdminUser[]>("/admin/api/users"));
  const groups = useAsync(() => get<AdminGroup[]>("/admin/api/groups"));
  const [selected, setSelected] = useState<DeviceGrant[]>(device.access);
  const [filter, setFilter] = useState("");
  const [busy, setBusy] = useState(false);

  const options: Option[] = useMemo(() => {
    const out: Option[] = [];
    for (const g of groups.data ?? []) {
      if (g.source === "policy") {
        out.push({ key: `policy_group:${g.name}`, kind: "policy_group", principal: g.name, label: `group:${g.name}`, sub: `policy group · ${g.members.length} member${g.members.length === 1 ? "" : "s"}` });
      } else if (g.id) {
        out.push({ key: `directory_group:${g.id}`, kind: "directory_group", principal: String(g.id), label: `group:${g.name}`, sub: `${g.source.toUpperCase()} group · ${g.members.length} member${g.members.length === 1 ? "" : "s"}` });
      }
    }
    for (const u of users.data ?? []) {
      if (u.disabled) continue;
      out.push({ key: `user:${u.id}`, kind: "user", principal: String(u.id), label: u.email || u.name, sub: u.displayName || "user" });
    }
    return out;
  }, [users.data, groups.data]);

  const isSel = (o: { kind: string; principal: string }) => selected.some((s) => s.kind === o.kind && s.principal === o.principal);
  const toggle = (o: Option) =>
    setSelected((cur) => (isSel(o) ? cur.filter((s) => !(s.kind === o.kind && s.principal === o.principal)) : [...cur, { kind: o.kind, principal: o.principal, label: o.label }]));

  const dirty = JSON.stringify([...selected].map((s) => s.kind + s.principal).sort()) !== JSON.stringify([...device.access].map((s) => s.kind + s.principal).sort());
  const needle = filter.trim().toLowerCase();
  const shown = options.filter((o) => !needle || o.label.toLowerCase().includes(needle) || o.sub.toLowerCase().includes(needle));

  return (
    <section className="drawer-section">
      <h3>Who can access this device</h3>
      <p className="muted small">
        With device restriction on, a device is visible only to its owner and to the users and groups below. Group membership follows your identity provider.
      </p>
      <div className="chip-row">
        {selected.length ? (
          selected.map((s) => (
            <span className="chip" key={s.kind + s.principal}>
              {s.label}
              {canEdit && (
                <button type="button" aria-label={`Remove ${s.label}`} onClick={() => setSelected((c) => c.filter((x) => !(x.kind === s.kind && x.principal === s.principal)))}>
                  ×
                </button>
              )}
            </span>
          ))
        ) : (
          <span className="muted">Only the owner{device.owner === "tagged" ? " (none: tagged device)" : ""}.</span>
        )}
      </div>
      {canEdit && (
        <>
          <input className="search mt" type="search" placeholder="Find a user or group" value={filter} onChange={(e) => setFilter(e.target.value)} />
          <div className="picker">
            {users.loading || groups.loading ? (
              <Spinner />
            ) : shown.length === 0 ? (
              <div className="muted pad">Nothing matches.</div>
            ) : (
              shown.map((o) => (
                <label key={o.key} className="picker-row">
                  <input type="checkbox" checked={isSel(o)} onChange={() => toggle(o)} />
                  <span>
                    <span>{o.label}</span>
                    <span className="muted small"> · {o.sub}</span>
                  </span>
                </label>
              ))
            )}
          </div>
          <div className="row-end">
            <Button variant="ghost" disabled={!dirty} onClick={() => setSelected(device.access)}>
              Reset
            </Button>
            <Button
              variant="primary"
              disabled={!dirty}
              busy={busy}
              onClick={async () => {
                setBusy(true);
                try {
                  await put(`/admin/api/devices/${device.id}/access`, { access: selected.map(({ kind, principal }) => ({ kind, principal })) });
                  toast.ok("Access updated");
                  onSaved();
                } catch (e) {
                  toast.error(e);
                } finally {
                  setBusy(false);
                }
              }}
            >
              Save access
            </Button>
          </div>
        </>
      )}
    </section>
  );
}

function DeviceDrawer({ device, onClose, onChanged }: { device: AdminDevice; onClose: () => void; onChanged: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const [name, setName] = useState(device.name);
  const [tags, setTags] = useState(device.tags);
  const [routes, setRoutes] = useState(device.approvedRoutes);
  const [confirm, setConfirm] = useState<"expire" | "delete" | null>(null);
  const [busy, setBusy] = useState("");

  const run = async (what: string, fn: () => Promise<unknown>, msg: string) => {
    setBusy(what);
    try {
      await fn();
      toast.ok(msg);
      onChanged();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy("");
    }
  };

  const allRoutes = Array.from(new Set([...device.announcedRoutes, ...device.approvedRoutes]));

  return (
    <Modal title={device.name} onClose={onClose} wide>
      <div className="kv">
        <div>Status</div>
        <div>
          <Status d={device} />
        </div>
        <div>Owner</div>
        <div>{device.owner === "tagged" ? <Badge tone="violet">Tagged device</Badge> : device.owner}</div>
        <div>Addresses</div>
        <div className="mono">{device.ips.join(", ")}</div>
        <div>OS / client</div>
        <div>
          {device.os || "—"} {device.clientVersion && <span className="muted">· {device.clientVersion}</span>}
        </div>
        <div>Last seen</div>
        <div>{device.online ? "now" : timeAgo(device.lastSeen)}</div>
        <div>Key expiry</div>
        <div>{device.expiry ? `${dateTime(device.expiry)} (${timeAgo(device.expiry)})` : "never"}</div>
        <div>Added</div>
        <div>{dateTime(device.createdAt)}</div>
      </div>

      <AccessEditor device={device} onSaved={onChanged} />

      {canEdit && (
        <>
          <section className="drawer-section">
            <h3>Name</h3>
            <div className="inline-form">
              <input value={name} onChange={(e) => setName(e.target.value)} />
              <Button disabled={name === device.name || !name.trim()} busy={busy === "rename"} onClick={() => run("rename", () => post(`/api/v1/node/${device.id}/rename/${encodeURIComponent(name.trim())}`), "Renamed")}>
                Rename
              </Button>
            </div>
          </section>

          <section className="drawer-section">
            <h3>Tags</h3>
            <p className="muted small">
              Tags must be owned in the policy (tagOwners). A tagged device belongs to its tags instead of a user. <Link to="/policy">Access policy</Link>
            </p>
            <ChipInput values={tags} onChange={setTags} placeholder="tag:server" validate={(v) => (v.startsWith("tag:") ? null : "Tags start with tag:")} />
            <div className="row-end">
              <Button disabled={JSON.stringify(tags) === JSON.stringify(device.tags)} busy={busy === "tags"} onClick={() => run("tags", () => post(`/api/v1/node/${device.id}/tags`, { tags }), "Tags updated")}>
                Save tags
              </Button>
            </div>
          </section>

          {allRoutes.length > 0 && (
            <section className="drawer-section">
              <h3>Subnet routes and exit node</h3>
              {allRoutes.map((r) => (
                <label key={r} className="picker-row">
                  <input type="checkbox" checked={routes.includes(r)} onChange={(e) => setRoutes((cur) => (e.target.checked ? [...cur, r] : cur.filter((x) => x !== r)))} />
                  <span className="mono">{r}</span>
                  {(r === "0.0.0.0/0" || r === "::/0") && <Badge tone="blue">exit node</Badge>}
                  {!device.announcedRoutes.includes(r) && <Badge tone="amber">no longer advertised</Badge>}
                </label>
              ))}
              <div className="row-end">
                <Button busy={busy === "routes"} disabled={JSON.stringify([...routes].sort()) === JSON.stringify([...device.approvedRoutes].sort())} onClick={() => run("routes", () => post(`/api/v1/node/${device.id}/approve_routes`, { routes }), "Routes updated")}>
                  Save routes
                </Button>
              </div>
            </section>
          )}

          <section className="drawer-section danger-zone">
            <h3>Danger zone</h3>
            <div className="row-between">
              <div>
                <div>Expire key</div>
                <div className="muted small">Disconnects the device until someone signs in on it again.</div>
              </div>
              <Button onClick={() => setConfirm("expire")}>Expire</Button>
            </div>
            <div className="row-between">
              <div>
                <div>Delete device</div>
                <div className="muted small">Removes it from the network and drops its access grants.</div>
              </div>
              <Button variant="danger" onClick={() => setConfirm("delete")}>
                Delete
              </Button>
            </div>
          </section>
        </>
      )}

      {confirm === "expire" && (
        <Confirm title={`Expire ${device.name}?`} confirmLabel="Expire key" onClose={() => setConfirm(null)} onConfirm={async () => {
          await post(`/api/v1/node/${device.id}/expire`, {});
          toast.ok("Key expired");
          onChanged();
        }}>
          The device disconnects now and must sign in again to rejoin.
        </Confirm>
      )}
      {confirm === "delete" && (
        <Confirm title={`Delete ${device.name}?`} confirmLabel="Delete device" danger onClose={() => setConfirm(null)} onConfirm={async () => {
          await del(`/api/v1/node/${device.id}`);
          toast.ok("Device deleted");
          onClose();
          onChanged();
        }}>
          The device is removed from the network. This cannot be undone.
        </Confirm>
      )}
    </Modal>
  );
}

export default function DevicesPage() {
  const { data, error, loading, reload } = useAsync(() => get<AdminDevice[]>("/admin/api/devices"));
  const [q, setQ] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data ?? [])
      .filter((d) => !needle || [d.name, d.hostname, d.owner, d.os, ...d.ips, ...d.tags, ...d.access.map((a) => a.label)].some((s) => s?.toLowerCase().includes(needle)))
      .sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name));
  }, [data, q]);

  const open = data?.find((d) => d.id === openId);

  return (
    <>
      <PageHeader title="Devices" description="Every machine on the network. Open a device to choose who can access it." />
      <div className="toolbar">
        <SearchBox value={q} onChange={setQ} placeholder="Search name, owner, address, tag or grantee" />
      </div>
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty title={q ? "No devices match" : "No devices yet"}>Devices appear when someone installs the Tailscale client and signs in with this server.</Empty>
      ) : (
        <div className="table-wrap card">
          <table>
            <thead>
              <tr>
                <th>Device</th>
                <th>Owner</th>
                <th>Address</th>
                <th>Access</th>
                <th>Last seen</th>
                <th>Status</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((d) => (
                <tr key={d.id} className="row-click" onClick={() => setOpenId(d.id)} tabIndex={0} onKeyDown={(e) => e.key === "Enter" && setOpenId(d.id)}>
                  <td>
                    <div>{d.name}</div>
                    <div className="muted small">
                      {d.os || "unknown OS"}
                      {d.tags.length > 0 && " · " + d.tags.join(", ")}
                      {d.announcedRoutes.length > d.approvedRoutes.length && <Badge tone="amber">routes pending</Badge>}
                    </div>
                  </td>
                  <td>{d.owner === "tagged" ? <Badge tone="violet">tagged</Badge> : d.owner}</td>
                  <td className="mono small">{d.ips[0]}</td>
                  <td>
                    <div className="chip-row">
                      {d.access.length ? d.access.slice(0, 3).map((a) => <span key={a.kind + a.principal} className="chip chip-static">{a.label}</span>) : <span className="muted">owner only</span>}
                      {d.access.length > 3 && <span className="muted small">+{d.access.length - 3}</span>}
                    </div>
                  </td>
                  <td className="muted">{d.online ? "now" : timeAgo(d.lastSeen)}</td>
                  <td>
                    <Status d={d} />
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {open && <DeviceDrawer device={open} onClose={() => setOpenId(null)} onChanged={reload} />}
    </>
  );
}
