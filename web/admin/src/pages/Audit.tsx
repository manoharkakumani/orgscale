import { useState } from "react";
import { get, type AuditEvent } from "../api";
import { Badge, Button, Empty, ErrorBox, PageHeader, SearchBox, Spinner, dateTime, timeAgo, useAsync, useToast } from "../components/ui";

const kindTone: Record<string, "blue" | "green" | "violet" | "amber" | "neutral"> = {
  admin: "blue",
  api_key: "violet",
  scim: "green",
  system: "amber",
};

const describe: Record<string, string> = {
  "admin.login": "Signed in to the console",
  "admin.login.denied": "Was refused console access",
  "admin.logout": "Signed out",
  "user.disable": "Disabled user",
  "user.enable": "Enabled user",
  "user.remove": "Removed user",
  "device.access.update": "Changed device access",
  "settings.login_rules.update": "Changed sign-in rules",
  "settings.roles.update": "Changed console access",
  "settings.device_restriction.update": "Changed access control",
  "settings.sso.update": "Changed single sign-on",
  "admin.sessions.end": "Signed out sessions",
  "setup.link_created": "Made a setup link",
  "setup.link_rejected": "Used an invalid setup link",
  "setup.started": "Opened setup",
  "setup.sso": "Set up single sign-on",
  "setup.finished": "Finished setup",
  "tag.save": "Saved server tag",
  "tag.delete": "Deleted server tag",
  "tag.enroll_key": "Made a server install key",
  "rule.create": "Added access rule",
  "rule.update": "Changed access rule",
  "rule.delete": "Deleted access rule",
  "group.create": "Created group",
  "group.update": "Changed group",
  "group.delete": "Deleted group",
  "device.bulk_tags": "Turned devices into servers",
  "device.bulk_access": "Shared devices",
  "scim.token.create": "Created SCIM token",
  "scim.token.delete": "Turned off SCIM",
  "scim.user.create": "Provisioned user",
  "scim.user.update": "Updated user",
  "scim.group.create": "Created group",
  "scim.group.update": "Updated group",
  "scim.group.replace": "Replaced group",
  "scim.group.delete": "Deleted group",
  "google_sync.update": "Changed Google sync",
  "google_sync.run": "Synced Google Workspace",
  "entra_sync.update": "Changed Microsoft Entra sync",
  "entra_sync.run": "Synced Microsoft Entra ID",
  "user.role": "Changed console role",
  "api.post": "API: create / action",
  "api.put": "API: update",
  "api.delete": "API: delete",
  "api.patch": "API: update",
};

// detailText makes stored details readable, e.g. a switch's true/false.
function detailText(e: AuditEvent): string {
  if (e.action === "settings.device_restriction.update") return e.detail === "true" ? "Turned on" : "Turned off";
  return e.detail || "";
}

export default function AuditPage() {
  const toast = useToast();
  const { data, error, loading, reload, setData } = useAsync(() => get<AuditEvent[]>("/admin/api/audit?limit=100"));
  const [q, setQ] = useState("");
  const [more, setMore] = useState(true);
  const [busy, setBusy] = useState(false);

  const needle = q.trim().toLowerCase();
  const rows = (data ?? []).filter((e) => !needle || [e.actor, e.action, e.target, e.detail, describe[e.action]].some((s) => s?.toLowerCase().includes(needle)));

  const loadMore = async () => {
    if (!data?.length) return;
    setBusy(true);
    try {
      const older = await get<AuditEvent[]>(`/admin/api/audit?limit=100&before=${data[data.length - 1].id}`);
      setData([...data, ...older]);
      setMore(older.length === 100);
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader title="Audit log" description="Every change made in the console, through the API, by SCIM or by directory sync, with who made it." actions={<Button onClick={reload}>Refresh</Button>} />
      <div className="toolbar">
        <SearchBox value={q} onChange={setQ} placeholder="Search actor, action or target" />
      </div>
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : rows.length === 0 ? (
        <Empty title="Nothing logged yet" />
      ) : (
        <div className="table-wrap card">
          <table>
            <thead>
              <tr>
                <th>When</th>
                <th>Who</th>
                <th>What</th>
                <th>Target</th>
                <th>Details</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((e) => (
                <tr key={e.id}>
                  <td className="muted nowrap" title={dateTime(e.createdAt)}>
                    {timeAgo(e.createdAt)}
                  </td>
                  <td>
                    <div className="cell-stack">
                      <span>{e.actor}</span>
                      <Badge tone={kindTone[e.actorKind] ?? "neutral"}>{e.actorKind.replace("_", " ")}</Badge>
                    </div>
                  </td>
                  <td>{describe[e.action] ?? e.action}</td>
                  <td className="mono small">{e.target || "—"}</td>
                  <td className="small muted">{detailText(e)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
      {data && data.length >= 100 && more && !needle && (
        <div className="row-center mt">
          <Button busy={busy} onClick={loadMore}>
            Load older
          </Button>
        </div>
      )}
    </>
  );
}
