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
        <Stat label="Device visibility" value={data.deviceRestriction ? "Restricted" : "Open"} sub={data.deviceRestriction ? "own + assigned devices only" : "everyone sees everything"} to="/settings" />
      </div>

      <section className="card">
        <h2>Setup</h2>
        <ol className="steps">
          <Step done={data.ssoConfigured} title="Single sign-on" to="/settings">
            {data.ssoConfigured ? `Users sign in through ${data.ssoIssuer}.` : "Set oidc.issuer in the configuration file."}
          </Step>
          <Step done={directory} title="Automatic user sync" to="/settings#directory">
            {data.scimEnabled
              ? "SCIM provisioning is on: users and groups follow your identity provider."
              : data.googleSync
                ? `Google Workspace sync is on${data.googleSyncStatus?.lastRun ? `, last run ${timeAgo(data.googleSyncStatus.lastRun)}` : ""}.`
                : "Connect SCIM (Okta, Entra ID, OneLogin, Authentik) or Google Workspace so people who leave lose access automatically."}
          </Step>
          <Step done={data.deviceRestriction} title="Restrict device visibility" to="/settings#visibility">
            {data.deviceRestriction ? "Users only see their own devices and the devices assigned to them." : "Turn on so users only see their own and assigned devices."}
          </Step>
          <Step done={data.assignedDevices > 0} title="Assign shared devices" to="/devices">
            {data.assignedDevices > 0 ? `${data.assignedDevices} device${data.assignedDevices === 1 ? " is" : "s are"} shared with users or groups.` : "Give users or groups access to servers and shared machines from the Devices page."}
          </Step>
        </ol>
      </section>

      {data.googleSync && data.googleSyncStatus?.error && (
        <div className="callout callout-error">Google Workspace sync failed {timeAgo(data.googleSyncStatus.lastRun)}: {data.googleSyncStatus.error}</div>
      )}
    </>
  );
}
