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

export default function GroupsPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<AdminGroup[]>("/admin/api/groups"));
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<string | null>(null);
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
                const isOpen = open === key;
                return (
                  <Fragment key={key}>
                    <tr className="row-click" onClick={() => setOpen(isOpen ? null : key)}>
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
                    {isOpen && (
                      <tr className="row-detail">
                        <td colSpan={4}>
                          <div className="chip-row">
                            {g.members.length ? g.members.map((m) => <span key={m} className="chip chip-static">{m}</span>) : <span className="muted">No members.</span>}
                          </div>
                        </td>
                      </tr>
                    )}
                  </Fragment>
                );
              })}
            </tbody>
          </table>
        </div>
        </>
      )}
      {sheet && (
        <Modal title={sheet.source === "policy" ? `group:${sheet.name}` : sheet.displayName} onClose={() => setSheet(null)}>
          <p className="muted">
            {(sourceInfo[sheet.source] ?? sourceInfo.policy).label} · used in rules as <code>group:{sheet.name}</code>
          </p>
          <div className="group-title">Members</div>
          <div className="chip-row">{sheet.members.length ? sheet.members.map((m) => <span key={m} className="chip chip-static">{m}</span>) : <span className="muted">No members.</span>}</div>
          {sheet.source !== "console" && sheet.source !== "policy" && <p className="muted small mt">Membership comes from your identity provider; change it there.</p>}
          {canEdit && sheet.source === "console" && (
            <div className="sheet-actions">
              <Button onClick={() => { setEditing(sheet); setSheet(null); }}>Edit group</Button>
              <Button variant="danger" onClick={() => { setDeleting(sheet); setSheet(null); }}>Delete group</Button>
            </div>
          )}
        </Modal>
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
