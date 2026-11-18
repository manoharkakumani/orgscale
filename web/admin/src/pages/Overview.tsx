import { Link } from "react-router-dom";
import { get, type Overview } from "../api";
import { ErrorBox, PageHeader, Spinner, timeAgo, useAsync } from "../components/ui";

function Stat({ label, value, sub, to }: { label: string; value: number | string; sub?: string; to?: string }) {
  const body = (
    <>
      <div className="stat-label">{label}</div>
      <div className="stat-value">{value}</div>
      {sub && <div className="stat-sub muted">{sub}</div>}
    </>
  );
  return to ? (
    <Link to={to} className="stat card">
      {body}
    </Link>
  ) : (
    <div className="stat card">{body}</div>
  );
}

function Step({ done, title, children, to }: { done: boolean; title: string; children: React.ReactNode; to: string }) {
  return (
    <li className={`step ${done ? "is-done" : ""}`}>
      <span className="step-mark" aria-hidden>
        {done ? "✓" : ""}
      </span>
      <div>
        <Link to={to} className="step-title">
          {title}
        </Link>
        <div className="muted small">{children}</div>
      </div>
    </li>
  );
}

export default function OverviewPage() {
  const { data, error, loading, reload } = useAsync(() => get<Overview>("/admin/api/overview"));

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!data) return null;

  const directory = data.scimEnabled || data.googleSync;

  return (
    <>
      <PageHeader title="Overview" description="Your network at a glance." />

      {data.warnings?.map((w) => (
        <div key={w} className="callout callout-warn">
          {w}
        </div>
      ))}

      <div className="stats">
        <Stat label="Users" value={data.users} sub={data.disabledUsers ? `${data.disabledUsers} disabled` : "all active"} to="/users" />
        <Stat label="Devices" value={data.devices} sub={`${data.onlineDevices} online${data.expiredDevices ? `, ${data.expiredDevices} expired` : ""}`} to="/devices" />
        <Stat label="Groups" value={data.groups} sub="from your identity provider" to="/groups" />
        <Stat label="Servers" value={data.taggedDevices} sub="joined with a tag" to="/servers" />
        <Stat label="Access control" value={data.deviceRestriction ? "On" : "Off"} sub={data.deviceRestriction ? "only what the rules allow" : "everyone reaches everything"} to="/access" />
      </div>

      <section className="card">
        <h2>Setup</h2>
        <ol className="steps">
          <Step done={data.ssoConfigured} title="Single sign-on" to="/settings?section=sso">
            {data.ssoConfigured ? `People sign in through ${data.ssoIssuer}.` : "Connect your identity provider in Settings → Single sign-on."}
          </Step>
          <Step done={directory} title="Automatic user sync" to="/settings?section=directory">
            {data.scimEnabled
              ? "SCIM provisioning is on: users and groups follow your identity provider."
              : data.googleSync
                ? `Google Workspace sync is on${data.googleSyncStatus?.lastRun ? `, last run ${timeAgo(data.googleSyncStatus.lastRun)}` : ""}.`
                : "Connect SCIM (Okta, Entra ID, OneLogin, Authentik) or Google Workspace so people who leave lose access automatically."}
          </Step>
          <Step done={data.taggedDevices > 0} title="Add your servers" to="/servers">
            {data.taggedDevices > 0 ? `${data.taggedDevices} server${data.taggedDevices === 1 ? "" : "s"} joined with a tag.` : "Create a tag such as tag:databases and add servers with a one-line install command."}
          </Step>
          <Step done={data.deviceRestriction} title="Turn on access control" to="/access">
            {data.deviceRestriction ? "People only reach their own devices and what they have been given." : "So people only see their own devices and the servers they've been given."}
          </Step>
          <Step done={data.assignedDevices > 0} title="Give people access" to="/access">
            {data.assignedDevices > 0 ? `${data.assignedDevices} device${data.assignedDevices === 1 ? " is" : "s are"} shared with people or groups.` : "Share servers through their tag, or add access rules for groups."}
          </Step>
        </ol>
      </section>

      {data.googleSync && data.googleSyncStatus?.error && (
        <div className="callout callout-error">Google Workspace sync failed {timeAgo(data.googleSyncStatus.lastRun)}: {data.googleSyncStatus.error}</div>
      )}
    </>
  );
}
