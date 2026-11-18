import { useMemo, useState } from "react";
import { Link } from "react-router-dom";
import { del, get, post, put, type AdminDevice, type Principal, type ServerTag } from "../api";
import { useCanEdit } from "../App";
import { IconTile, MList, MRow } from "../components/mlist";
import { ensureTags, newTagsHint, normalizeTag, validTag } from "../components/tags";
import { PrincipalList, PrincipalPicker } from "../components/principals";
import { Badge, Button, ChipInput, Confirm, Empty, ErrorBox, Field, Icon, Modal, PageHeader, SearchBox, Segmented, Spinner, dateTime, timeAgo, useAsync, useToast } from "../components/ui";

function Status({ d }: { d: AdminDevice }) {
  if (d.expired) return <Badge tone="red">Expired</Badge>;
  return d.online ? <Badge tone="green">Online</Badge> : <Badge>Offline</Badge>;
}

// AccessSection says who can reach a device. A person's device is theirs
// alone unless shared; a server is reached through its tags' access lists.
function AccessSection({ device, tags, onSaved }: { device: AdminDevice; tags: ServerTag[]; onSaved: () => void }) {
  const canEdit = useCanEdit();
  const toast = useToast();
  const [editing, setEditing] = useState(false);
  const [selected, setSelected] = useState<Principal[]>(device.access);
  const [busy, setBusy] = useState(false);
  const isServer = device.owner === "tagged";
  const viaTags = tags.filter((t) => device.tags.includes(t.name));

  const save = async () => {
    setBusy(true);
    try {
      await put(`/admin/api/devices/${device.id}/access`, { access: selected.map(({ kind, principal }) => ({ kind, principal })) });
      toast.ok(selected.length ? "Sharing updated" : "No longer shared");
      setEditing(false);
      onSaved();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <section className="drawer-section">
      <div className="row-between" style={{ marginTop: 0 }}>
        <h3 style={{ margin: 0 }}>Who can reach it</h3>
        {canEdit && !editing && (
          <Button
            onClick={() => {
              setSelected(device.access);
              setEditing(true);
            }}
          >
            {device.access.length ? "Change sharing" : "Share…"}
          </Button>
        )}
      </div>

      {isServer ? (
        viaTags.map((t) => (
          <div key={t.name} className="access-line">
            <span className="muted small">Through {t.name}</span>
            <PrincipalList items={t.access} empty="nobody yet" max={6} />
            <Link className="small" to="/servers">
              Edit
            </Link>
          </div>
        ))
      ) : (
        <div className="access-line">
          <span className="muted small">Owner</span>
          <span className="chip chip-static">{device.owner}</span>
          <span className="muted small">and their other devices</span>
        </div>
      )}

      {!editing && device.access.length > 0 && (
        <div className="access-line">
          <span className="muted small">Shared with</span>
          <PrincipalList items={device.access} />
        </div>
      )}

      {!editing && !isServer && device.access.length === 0 && <p className="muted small">Nobody else can see or reach this device.</p>}

      {editing && (
        <>
          <p className="muted small mt">Share this device with people or groups. They can see and connect to it; group membership follows your identity provider.</p>
          <PrincipalPicker kinds={["directory_group", "policy_group", "user"]} value={selected} onChange={setSelected} placeholder="Search people and groups" height={220} />
          <div className="row-end">
            <Button variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
            <Button variant="primary" busy={busy} onClick={save}>
              Save
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
  const serverTags = useAsync(() => get<ServerTag[]>("/admin/api/tags"));
  const known = (serverTags.data ?? []).map((t) => t.name);
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
        <div>{device.owner === "tagged" ? <Badge tone="violet">Server ({device.tags.join(", ")})</Badge> : device.owner}</div>
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

      <AccessSection device={device} tags={serverTags.data ?? []} onSaved={onChanged} />

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

          {device.owner === "tagged" ? (
            <section className="drawer-section">
              <h3>Server tags</h3>
              <p className="muted small">This server belongs to its tags; each tag's access list on the Servers page decides who can reach it.</p>
              <ChipInput values={tags} onChange={setTags} placeholder="tag:servers" suggestions={(serverTags.data ?? []).map((t) => t.name)} normalize={normalizeTag} validate={validTag} />
              {newTagsHint(tags, known) && <div className="field-hint">{newTagsHint(tags, known)}</div>}
              <div className="row-end">
                <Button disabled={!tags.length || JSON.stringify(tags) === JSON.stringify(device.tags)} busy={busy === "tags"} onClick={() => run("tags", async () => { await ensureTags(tags, known); await post(`/api/v1/node/${device.id}/tags`, { tags }); }, "Tags updated")}>
                  Save tags
                </Button>
              </div>
            </section>
          ) : (
            <details className="disclosure drawer-section">
              <summary>Turn into a server</summary>
              <p className="muted small">
                For a shared machine that was set up with someone's account, such as a build box or a lab VM. It then belongs to a tag instead of {device.owner}, and the tag's access list decides who can reach it. {device.owner} keeps no special access.
              </p>
              <ChipInput values={tags} onChange={setTags} placeholder="tag:servers" suggestions={(serverTags.data ?? []).map((t) => t.name)} normalize={normalizeTag} validate={validTag} />
              {newTagsHint(tags, known) && <div className="field-hint">{newTagsHint(tags, known)}</div>}
              <div className="row-end">
                <Button disabled={!tags.length} busy={busy === "tags"} onClick={() => run("tags", async () => { await ensureTags(tags, known); await post(`/api/v1/node/${device.id}/tags`, { tags }); }, "Turned into a server")}>
                  Turn into a server
                </Button>
              </div>
            </details>
          )}

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

function BulkTags({ devices, onClose, onDone }: { devices: AdminDevice[]; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const tags = useAsync(() => get<ServerTag[]>("/admin/api/tags"));
  const [chosen, setChosen] = useState<string[]>([]);
  const [busy, setBusy] = useState(false);
  const personal = devices.filter((d) => d.owner !== "tagged");
  const known = (tags.data ?? []).map((t) => t.name);

  return (
    <Modal
      title={`Turn ${devices.length} device${devices.length === 1 ? "" : "s"} into servers`}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={busy}
            disabled={!chosen.length}
            onClick={async () => {
              setBusy(true);
              try {
                await ensureTags(chosen, known);
                await post("/admin/api/devices/bulk", { ids: devices.map((d) => d.id), action: "set_tags", tags: chosen });
                toast.ok(`Moved ${devices.length} device${devices.length === 1 ? "" : "s"} to ${chosen.join(", ")}`);
                onDone();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Turn into servers
          </Button>
        </>
      }
    >
      {tags.loading ? (
        <Spinner />
      ) : (
        <>
          {(tags.data ?? []).length > 0 && (
            <div className="choice-grid">
              {(tags.data ?? []).map((t) => (
                <button key={t.name} type="button" className={`choice ${chosen.includes(t.name) ? "active" : ""}`} onClick={() => setChosen((c) => (c.includes(t.name) ? c.filter((x) => x !== t.name) : [...c, t.name]))}>
                  <b>{t.name}</b>
                  <span className="muted small">{t.description || `${t.devices.length} server${t.devices.length === 1 ? "" : "s"}`}</span>
                </button>
              ))}
            </div>
          )}
          <Field label={(tags.data ?? []).length ? "Or a new tag" : "Tag"} hint={newTagsHint(chosen, known)}>
            <ChipInput values={chosen.filter((c) => !known.includes(c))} onChange={(fresh) => setChosen([...chosen.filter((c) => known.includes(c)), ...fresh])} placeholder="e.g. build-agents" normalize={normalizeTag} validate={validTag} />
          </Field>
          {personal.length > 0 && (
            <div className="callout callout-warn mt">
              <span>
                {personal.length} of these belong to a person ({Array.from(new Set(personal.map((d) => d.owner))).join(", ")}). After moving they become servers owned by the tag, and the person no longer owns them. Use this for shared machines that were set up with someone's account.
              </span>
            </div>
          )}
        </>
      )}
    </Modal>
  );
}

function BulkAccess({ devices, onClose, onDone }: { devices: AdminDevice[]; onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [who, setWho] = useState<Principal[]>([]);
  const [mode, setMode] = useState<"add_access" | "set_access">("add_access");
  const [busy, setBusy] = useState(false);

  return (
    <Modal
      title={`Share ${devices.length} device${devices.length === 1 ? "" : "s"}`}
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
            disabled={mode === "add_access" && !who.length}
            onClick={async () => {
              setBusy(true);
              try {
                await post("/admin/api/devices/bulk", { ids: devices.map((d) => d.id), action: mode, access: who.map(({ kind, principal }) => ({ kind, principal })) });
                toast.ok("Access updated");
                onDone();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            {mode === "add_access" ? "Give access" : "Replace access"}
          </Button>
        </>
      }
    >
      <Field label="Change">
        <Segmented
          value={mode}
          options={[
            { value: "add_access", label: "Add to current access" },
            { value: "set_access", label: "Replace current access" },
          ]}
          onChange={setMode}
        />
      </Field>
      <Field label="People and groups" hint="They can see and connect to the selected devices. Group membership follows your identity provider.">
        <PrincipalPicker kinds={["directory_group", "policy_group", "user"]} value={who} onChange={setWho} height={260} />
      </Field>
    </Modal>
  );
}

export default function DevicesPage() {
  const { data, error, loading, reload } = useAsync(() => get<AdminDevice[]>("/admin/api/devices"));
  const [q, setQ] = useState("");
  const [openId, setOpenId] = useState<number | null>(null);
  const [selected, setSelected] = useState<Set<number>>(new Set());
  const [bulk, setBulk] = useState<"tags" | "access" | null>(null);
  const [selecting, setSelecting] = useState(false);
  const toggleSel = (id: number) =>
    setSelected((cur) => {
      const next = new Set(cur);
      if (next.has(id)) next.delete(id);
      else next.add(id);
      return next;
    });
  const canEdit = useCanEdit();

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data ?? [])
      .filter((d) => !needle || [d.name, d.hostname, d.owner, d.os, ...d.ips, ...d.tags, ...d.access.map((a) => a.label)].some((s) => s?.toLowerCase().includes(needle)))
      .sort((a, b) => Number(b.online) - Number(a.online) || a.name.localeCompare(b.name));
  }, [data, q]);

  const open = data?.find((d) => d.id === openId);

  return (
    <>
      <PageHeader
        title="Devices"
        description="Every machine on the network. Open one to see who can reach it, or select several to share them."
        actions={
          canEdit && (
            <>
              <Link className="btn btn-primary" to="/servers?add=1">
                <Icon name="plus" /> Add server
              </Link>
              <Button
                className="phone-only"
                onClick={() => {
                  setSelecting((v) => !v);
                  setSelected(new Set());
                }}
              >
                {selecting ? "Done" : "Select"}
              </Button>
            </>
          )
        }
      />
      <div className="toolbar">
        <SearchBox value={q} onChange={setQ} placeholder="Search name, owner, address, tag or grantee" />

      </div>
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty title={q ? "No devices match" : "No devices yet"}>Devices appear when someone installs the Tailscale client and signs in with this server.</Empty>
      ) : (
        <>
        <MList>
          {rows.map((d) => {
            const server = d.owner === "tagged";
            return (
              <MRow
                key={d.id}
                selected={selected.has(d.id)}
                onClick={() => (selecting ? toggleSel(d.id) : setOpenId(d.id))}
                lead={
                  <>
                    {selecting && <input type="checkbox" readOnly checked={selected.has(d.id)} aria-label={`Select ${d.name}`} />}
                    <IconTile tone={server ? "violet" : "blue"}>
                      <Icon name={server ? "server" : "devices"} />
                    </IconTile>
                  </>
                }
                title={d.name}
                sub={server ? `${d.tags.join(", ")} · ${d.ips[0] ?? ""}` : `${d.owner} · ${d.os || "device"}`}
                trail={
                  d.expired ? (
                    <Badge tone="red">Expired</Badge>
                  ) : (
                    <span className="row" style={{ margin: 0, gap: 6 }}>
                      <span className={`dot ${d.online ? "dot-on" : ""}`} />
                      {d.online ? "Online" : "Offline"}
                    </span>
                  )
                }
              />
            );
          })}
        </MList>
        <div className="table-wrap card desktop-only">
          <table>
            <thead>
              <tr>
                {canEdit && (
                  <th className="check">
                    <input type="checkbox" aria-label="Select all" checked={rows.length > 0 && rows.every((d) => selected.has(d.id))} onChange={(e) => setSelected(e.target.checked ? new Set(rows.map((d) => d.id)) : new Set())} />
                  </th>
                )}
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
                <tr key={d.id} className={`row-click ${selected.has(d.id) ? "row-selected" : ""}`} onClick={() => setOpenId(d.id)} tabIndex={0} onKeyDown={(e) => e.key === "Enter" && setOpenId(d.id)}>
                  {canEdit && (
                    <td className="check" onClick={(e) => e.stopPropagation()}>
                      <input
                        type="checkbox"
                        aria-label={`Select ${d.name}`}
                        checked={selected.has(d.id)}
                        onChange={(e) =>
                          setSelected((cur) => {
                            const next = new Set(cur);
                            if (e.target.checked) next.add(d.id);
                            else next.delete(d.id);
                            return next;
                          })
                        }
                      />
                    </td>
                  )}
                  <td>
                    <div>{d.name}</div>
                    <div className="muted small">
                      {d.os || "unknown OS"}
                      {d.tags.length > 0 && " · " + d.tags.join(", ")}
                      {d.announcedRoutes.length > d.approvedRoutes.length && <Badge tone="amber">routes pending</Badge>}
                    </div>
                  </td>
                  <td>{d.owner === "tagged" ? <Badge tone="violet">server</Badge> : d.owner}</td>
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
        </>
      )}
      {selected.size > 0 && (
        <div className="bulkbar">
          <b>{selected.size} selected</b>
          <Button onClick={() => setBulk("access")}>
            <Icon name="users" /> Share…
          </Button>
          <Button onClick={() => setBulk("tags")}>
            <Icon name="tag" /> Turn into servers…
          </Button>
          <Button variant="ghost" onClick={() => setSelected(new Set())}>
            Clear
          </Button>
        </div>
      )}
      {bulk && (() => {
        const chosen = (data ?? []).filter((d) => selected.has(d.id));
        const done = () => {
          setSelected(new Set());
          void reload();
        };
        return bulk === "tags" ? <BulkTags devices={chosen} onClose={() => setBulk(null)} onDone={done} /> : <BulkAccess devices={chosen} onClose={() => setBulk(null)} onDone={done} />;
      })()}
      {open && <DeviceDrawer device={open} onClose={() => setOpenId(null)} onChanged={reload} />}
    </>
  );
}
