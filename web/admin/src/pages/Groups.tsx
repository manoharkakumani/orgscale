import { Fragment, useMemo, useState } from "react";
import { get, type AdminGroup } from "../api";
import { Badge, Empty, ErrorBox, PageHeader, SearchBox, Spinner, useAsync } from "../components/ui";

const sourceInfo: Record<AdminGroup["source"], { label: string; tone: "blue" | "green" | "violet" | "amber" }> = {
  scim: { label: "SCIM", tone: "green" },
  google: { label: "Google", tone: "blue" },
  oidc: { label: "SSO login", tone: "violet" },
  policy: { label: "Policy file", tone: "amber" },
};

export default function GroupsPage() {
  const { data, error, loading, reload } = useAsync(() => get<AdminGroup[]>("/admin/api/groups"));
  const [q, setQ] = useState("");
  const [open, setOpen] = useState<string | null>(null);

  const rows = useMemo(() => {
    const needle = q.trim().toLowerCase();
    return (data ?? []).filter((g) => !needle || g.name.toLowerCase().includes(needle) || g.displayName.toLowerCase().includes(needle) || g.members.some((m) => m.toLowerCase().includes(needle)));
  }, [data, q]);

  return (
    <>
      <PageHeader
        title="Groups"
        description={
          <>
            Groups come from your identity provider (SCIM, Google Workspace, or the groups claim at sign-in) and stay in sync with it. Use them to grant device access, or in the access policy as{" "}
            <code>group:name</code>.
          </>
        }
      />
      <div className="toolbar">
        <SearchBox value={q} onChange={setQ} placeholder="Search groups or members" />
      </div>
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty title={q ? "No groups match" : "No groups yet"}>
          Groups appear once SCIM or Google sync is connected, or when users sign in and your identity provider sends a <code>groups</code> claim.
        </Empty>
      ) : (
        <div className="table-wrap card">
          <table>
            <thead>
              <tr>
                <th>Policy name</th>
                <th>Name in identity provider</th>
                <th>Source</th>
                <th className="num">Members</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((g) => {
                const key = `${g.source}:${g.id ?? g.name}`;
                const isOpen = open === key;
                return (
                  <Fragment key={key}>
                    <tr className="row-click" onClick={() => setOpen(isOpen ? null : key)}>
                      <td className="mono">group:{g.name}</td>
                      <td>{g.source === "policy" ? <span className="muted">—</span> : g.displayName}</td>
                      <td>
                        <Badge tone={sourceInfo[g.source].tone}>{sourceInfo[g.source].label}</Badge>
                      </td>
                      <td className="num">{g.members.length}</td>
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
      )}
    </>
  );
}
