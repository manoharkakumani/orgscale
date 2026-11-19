import { Fragment, useMemo, useState } from "react";
import { del, get, post, put, type AdminGroup, type Principal } from "../api";
import { useCanEdit } from "../App";
import { PrincipalPicker } from "../components/principals";
import { IconTile, MList, MRow } from "../components/mlist";
import { Badge, Button, Confirm, Empty, ErrorBox, Field, Icon, Modal, PageHeader, SearchBox, Spinner, useAsync, useToast } from "../components/ui";

const sourceInfo: Record<AdminGroup["source"], { label: string; tone: "blue" | "green" | "violet" | "amber" | "teal" }> = {
  console: { label: "Made here", tone: "teal" },
  scim: { label: "SCIM", tone: "green" },
  google: { label: "Google", tone: "blue" },
  entra: { label: "Microsoft Entra", tone: "blue" },
  oidc: { label: "SSO login", tone: "violet" },
  policy: { label: "Policy file", tone: "amber" },
};

function GroupEditor({ group, onClose, onSaved }: { group?: AdminGroup; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [name, setName] = useState(group?.displayName ?? "");
  const [members, setMembers] = useState<Principal[]>((group?.memberIds ?? []).map((id, i) => ({ kind: "user", principal: String(id), label: group?.members[i] })));
  const [busy, setBusy] = useState(false);

  return (
    <Modal
      title={group ? `Edit ${group.displayName}` : "New group"}
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
            disabled={!name.trim()}
            onClick={async () => {
              setBusy(true);
              try {
                const body = { name, memberIds: members.map((m) => Number(m.principal)) };
                if (group?.id) await put(`/admin/api/groups/${group.id}`, body);
                else await post("/admin/api/groups", body);
                toast.ok(group ? "Group saved" : "Group created");
                onSaved();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            {group ? "Save" : "Create group"}
          </Button>
        </>
      }
    >
      <Field label="Name" hint="e.g. Web Team, Contractors, On-call.">
        <input value={name} onChange={(e) => setName(e.target.value)} autoFocus placeholder="Group name" />
      </Field>
      <Field label="Members" hint="Members gain the access given to the group on Servers, Devices and Access.">
        <PrincipalPicker kinds={["user"]} value={members} onChange={setMembers} placeholder="Search people" height={260} />
      </Field>
    </Modal>
  );
}

/** GroupSheet shows a group's members, searchable, however many there are. */
function GroupSheet({ group, canEdit, onClose, onEdit, onDelete }: { group: AdminGroup; canEdit: boolean; onClose: () => void; onEdit: () => void; onDelete: () => void }) {
  const [q, setQ] = useState("");
  const info = sourceInfo[group.source] ?? sourceInfo.policy;
  const members = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return [...group.members].sort((a, b) => a.localeCompare(b)).filter((m) => !needle || m.toLowerCase().includes(needle));
  }, [group.members, q]);

  return (
    <Modal title={group.source === "policy" ? `group:${group.name}` : group.displayName} onClose={onClose} wide>
      <p className="muted">
        <Badge tone={info.tone}>{info.label}</Badge> Used in rules as <code>group:{group.name}</code>
      </p>
      <div className="row-between mt">
        <div className="group-title" style={{ margin: 0 }}>
          {group.members.length} member{group.members.length === 1 ? "" : "s"}
        </div>
        {group.members.length > 8 && <SearchBox value={q} onChange={setQ} placeholder="Search members" />}
      </div>
      {group.members.length === 0 ? (
        <p className="muted">No members.</p>
      ) : (
        <div className="group member-list">
          {members.map((m) => (
            <div className="prow" key={m}>
              <div className="cell-user">
                <span className="avatar avatar-fallback">{m.slice(0, 1).toUpperCase()}</span>
                <span className="ellipsis">{m}</span>
              </div>
            </div>
          ))}
          {members.length === 0 && <div className="prow muted">Nobody matches “{q}”.</div>}
        </div>
      )}
      {group.source !== "console" && group.source !== "policy" && <p className="muted small mt">Membership comes from your identity provider; change it there.</p>}
      {canEdit && group.source === "console" && (
        <div className="sheet-actions">
          <Button onClick={onEdit}>Edit group</Button>
          <Button variant="danger" onClick={onDelete}>
            Delete group
          </Button>
        </div>
      )}
    </Modal>
  );
}

export default function GroupsPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<AdminGroup[]>("/admin/api/groups"));
  const [q, setQ] = useState("");
  const [editing, setEditing] = useState<AdminGroup | "new" | null>(null);
  const [deleting, setDeleting] = useState<AdminGroup | null>(null);
  const [sheet, setSheet] = useState<AdminGroup | null>(null);

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data ?? []).filter((g) => !needle || g.name.toLowerCase().includes(needle) || g.displayName.toLowerCase().includes(needle) || g.members.some((m) => m.toLowerCase().includes(needle)));
  }, [data, q]);

  return (
    <>
      <PageHeader
        title="Groups"
        description="Give access to groups instead of individual people. Groups from your identity provider (SCIM, Google Workspace, SSO) stay in sync with it; you can also make your own here."
        actions={
          canEdit && (
            <Button variant="primary" onClick={() => setEditing("new")}>
              <Icon name="plus" /> New group
            </Button>
          )
        }
      />
      <div className="toolbar">
        <SearchBox value={q} onChange={setQ} placeholder="Search groups or members" />
      </div>
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty title={q ? "No groups match" : "No groups yet"}>Create a group here, or connect SCIM or Google Workspace in Settings to bring in your identity provider's groups.</Empty>
      ) : (
        <>
        <MList>
          {rows.map((g) => {
            const info = sourceInfo[g.source] ?? sourceInfo.policy;
            return (
              <MRow
                key={`${g.source}:${g.id ?? g.name}`}
                onClick={() => setSheet(g)}
                lead={
                  <IconTile tone={g.source === "console" ? "teal" : g.source === "scim" ? "green" : g.source === "google" ? "blue" : g.source === "oidc" ? "violet" : "orange"}>
                    <Icon name="users" />
                  </IconTile>
                }
                title={g.source === "policy" ? `group:${g.name}` : g.displayName}
                sub={`${info.label} · ${g.members.length} member${g.members.length === 1 ? "" : "s"}`}
              />
            );
          })}
        </MList>
        <div className="table-wrap card desktop-only">
          <table>
            <thead>
              <tr>
                <th>Group</th>
                <th>Source</th>
                <th className="num">Members</th>
                <th />
              </tr>
            </thead>
            <tbody>
              {rows.map((g) => {
                const key = `${g.source}:${g.id ?? g.name}`;
                return (
                  <Fragment key={key}>
                    <tr className="row-click" onClick={() => setSheet(g)}>
                      <td>
                        <div className="cell-stack">
                          <span>{g.source === "policy" ? `group:${g.name}` : g.displayName}</span>
                          <span className="muted small mono">group:{g.name}</span>
                        </div>
                      </td>
                      <td>
                        <Badge tone={(sourceInfo[g.source] ?? sourceInfo.policy).tone}>{(sourceInfo[g.source] ?? sourceInfo.policy).label}</Badge>
                      </td>
                      <td className="num">{g.members.length}</td>
                      <td className="actions" onClick={(e) => e.stopPropagation()}>
                        {canEdit && g.source === "console" && (
                          <>
                            <Button onClick={() => setEditing(g)}>Edit</Button>
                            <button className="icon-btn" aria-label={`Delete ${g.displayName}`} onClick={() => setDeleting(g)}>
                              <Icon name="trash" />
                            </button>
                          </>
                        )}
                      </td>
                    </tr>
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
        </>
      )}
      {sheet && (
        <GroupSheet
          group={sheet}
          canEdit={canEdit}
          onClose={() => setSheet(null)}
          onEdit={() => {
            setEditing(sheet);
            setSheet(null);
          }}
          onDelete={() => {
            setDeleting(sheet);
            setSheet(null);
          }}
        />
      )}
      {editing && <GroupEditor group={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} onSaved={() => void reload()} />}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.displayName}?`}
          confirmLabel="Delete group"
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await del(`/admin/api/groups/${deleting.id}`);
            toast.ok("Group deleted");
            void reload();
          }}
        >
          Members lose any access they had only through this group. The people themselves are not affected.
        </Confirm>
      )}
    </>
  );
}
