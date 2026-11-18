import { Link } from "react-router-dom";
import { get, type Overview } from "../api";
import { useMe } from "../App";
import { CopyText, ErrorBox, PageHeader, Spinner, timeAgo, useAsync } from "../components/ui";

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
  const me = useMe();
  const { data, error, loading, reload } = useAsync(() => get<Overview>("/admin/api/overview"));
  const base = me.serverUrl.replace(/\/+$/, "");

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!data) return null;

  const directory = data.scimEnabled || data.googleSync || data.entraSync;

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
            {data.entraSync
              ? "Microsoft Entra sync is on: users and groups follow Entra."
              : data.scimEnabled
              ? "SCIM provisioning is on: users and groups follow your identity provider."
              : data.googleSync
                ? `Google Workspace sync is on${data.googleSyncStatus?.lastRun ? `, last run ${timeAgo(data.googleSyncStatus.lastRun)}` : ""}.`
                : "Connect Microsoft Entra, Google Workspace or SCIM (Okta, OneLogin, Authentik) so people who leave lose access automatically."}
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

      <section className="card">
        <h2>Connect people's devices</h2>
        <p className="muted small">Send everyone this link. It shows each platform's steps and downloads the settings, so nobody types a command.</p>
        <CopyText value={`${base}/connect`} />
        <details className="mt">
          <summary>Company-managed devices (Intune, Jamf, Kandji)</summary>
          <ul className="small">
            <li>
              <b>Mac</b>: upload <a href={`${base}/apple/macos`} download="orgscale.mobileconfig">orgscale.mobileconfig</a> as a custom configuration profile.
            </li>
            <li>
              <b>Windows</b>: set the registry value <code>HKLM\SOFTWARE\Policies\Tailscale\LoginURL</code> to <code>{base}</code> (Settings catalog, OMA-URI or a script), or deploy <a href={`${base}/windows/orgscale.reg`}>orgscale.reg</a>.
            </li>
            <li>
              <b>iPhone and iPad</b>: an app configuration policy for the Tailscale app with <code>ControlURL</code> = <code>{base}</code>. <b>Android</b>: managed configuration with <code>LoginURL</code> = <code>{base}</code>.
            </li>
          </ul>
          <p className="muted small">People then just open Tailscale and click Log in. Servers join from Servers → Add server instead.</p>
        </details>
      </section>

      {data.googleSync && data.googleSyncStatus?.error && (
        <div className="callout callout-error">Google Workspace sync failed {timeAgo(data.googleSyncStatus.lastRun)}: {data.googleSyncStatus.error}</div>
      )}
    </>
  );
}
