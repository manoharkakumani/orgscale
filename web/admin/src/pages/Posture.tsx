import { useEffect, useMemo, useState } from "react";
import { Link } from "react-router-dom";
import {
  get,
  post,
  put,
  type DevicePosture,
  type OSRequirement,
  type PostureInfo,
  type PostureMode,
  type PostureRules,
} from "../api";
import { useCanEdit } from "../App";
import {
  Badge,
  Button,
  Confirm,
  Empty,
  ErrorBox,
  Group,
  PRow,
  PageHeader,
  SearchBox,
  Segmented,
  Spinner,
  Toggle,
  timeAgo,
  useAsync,
  useToast,
} from "../components/ui";

const oses: { key: string; label: string; placeholder?: string; hint?: string }[] = [
  { key: "macOS", label: "macOS", placeholder: "14", hint: "e.g. 14 for Sonoma" },
  { key: "windows", label: "Windows", placeholder: "10.0.22000", hint: "10.0.22000 is Windows 11" },
  { key: "iOS", label: "iOS and iPadOS", placeholder: "17" },
  { key: "android", label: "Android", placeholder: "13" },
  { key: "linux", label: "Linux", hint: "Linux reports no comparable version; it can only be allowed or blocked." },
];

const versionRE = /^\d+(\.\d+)*$/;

const intuneLabel: Record<string, string> = {
  compliant: "Intune compliant",
  noncompliant: "Intune non-compliant",
  notManaged: "Not in Intune",
  unavailable: "Intune unknown",
  noSerial: "No serial number",
  duplicate: "Serial number reported twice",
  otherOwner: "Someone else's in Intune",
};

function sameRules(a: PostureRules, b: PostureRules): boolean {
  return JSON.stringify(normalize(a)) === JSON.stringify(normalize(b));
}

function normalize(r: PostureRules): PostureRules {
  const os: Record<string, OSRequirement> = {};
  for (const o of oses) {
    const v = r.os[o.key];
    if (v && (v.blocked || v.minVersion.trim()))
      os[o.key] = { blocked: v.blocked, minVersion: v.blocked ? "" : v.minVersion.trim() };
  }
  return { mode: r.mode, minTailscaleVersion: r.minTailscaleVersion.trim(), os, requireIntune: r.requireIntune };
}

function DeviceRow({ d }: { d: DevicePosture }) {
  const facts = [
    d.osVersion ? `${d.osLabel} ${d.osVersion}` : d.osLabel,
    d.tailscaleVersion && `Tailscale ${d.tailscaleVersion}`,
    d.intune && intuneLabel[d.intune],
  ]
    .filter(Boolean)
    .join(" · ");
  return (
    <PRow
      label={
        <span>
          {d.blocked ? (
            <Badge tone="red">Cut off</Badge>
          ) : d.passing ? (
            <Badge tone="green">Passing</Badge>
          ) : (
            <Badge tone="amber">Failing</Badge>
          )}{" "}
          <b>{d.name}</b> <span className="muted small">{d.user}</span>
        </span>
      }
      sub={
        <>
          {facts}
          {!d.online && <span className="muted"> · offline</span>}
          {d.reasons.length > 0 && <div className="text-warn">{d.reasons.join("; ")}</div>}
        </>
      }
    />
  );
}

export default function PosturePage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload, setData } = useAsync(() => get<PostureInfo>("/admin/api/posture"));
  const [rules, setRules] = useState<PostureRules | null>(null);
  const [busy, setBusy] = useState(false);
  const [checking, setChecking] = useState(false);
  const [confirmEnforce, setConfirmEnforce] = useState(false);
  const [filter, setFilter] = useState<"all" | "failing">("all");
  const [q, setQ] = useState("");

  useEffect(() => {
    if (data && !rules) setRules(data.rules);
  }, [data, rules]);

  // Refresh the device list while the page is open.
  useEffect(() => {
    const id = window.setInterval(async () => {
      try {
        setData(await get<PostureInfo>("/admin/api/posture"));
      } catch {
        /* keep the last list */
      }
    }, 30000);
    return () => window.clearInterval(id);
  }, [setData]);

  const dirty = !!(data && rules && !sameRules(rules, data.rules));
  const invalid =
    !!rules &&
    ((rules.minTailscaleVersion.trim() !== "" && !versionRE.test(rules.minTailscaleVersion.trim())) ||
      Object.values(rules.os).some(
        (o) => !o.blocked && o.minVersion.trim() !== "" && !versionRE.test(o.minVersion.trim()),
      ));

  const devices = useMemo(() => {
    const list = data?.devices ?? [];
    const needle = q.trim().toLowerCase();
    return list.filter(
      (d) =>
        (filter === "all" || !d.passing) &&
        (!needle || d.name.toLowerCase().includes(needle) || d.user.toLowerCase().includes(needle)),
    );
  }, [data, filter, q]);

  const failing = (data?.devices ?? []).filter((d) => !d.passing).length;

  const save = async () => {
    if (!rules) return;
    setBusy(true);
    try {
      const next = await put<PostureInfo>("/admin/api/posture", normalize(rules));
      setData(next);
      setRules(next.rules);
      toast.ok(next.rules.mode === "enforce" ? "Requirements enforced" : "Requirements saved");
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const setOS = (key: string, v: Partial<OSRequirement>) => {
    if (!rules) return;
    const cur = rules.os[key] ?? { blocked: false, minVersion: "" };
    setRules({ ...rules, os: { ...rules.os, [key]: { ...cur, ...v } } });
  };

  return (
    <>
      <PageHeader
        title="Device posture"
        description="Requirements the devices people sign in with must meet: operating system, versions and Intune compliance. Servers (tagged devices) are not checked."
        actions={
          <Button
            busy={checking}
            onClick={async () => {
              setChecking(true);
              try {
                setData(await post<PostureInfo>("/admin/api/posture/check"));
              } catch (e) {
                toast.error(e);
              } finally {
                setChecking(false);
              }
            }}
          >
            Check now
          </Button>
        }
      />
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : data && rules ? (
        <>
          {rules.mode === "enforce" && !data.accessControl && (
            <div className="callout callout-warn">
              Access control is off, so every device reaches everything and failing devices cannot be cut off. Turn it
              on under <Link to="/access">Access</Link>.
            </div>
          )}

          <Group title="Requirements">
            <PRow
              label="When a device fails"
              sub={
                rules.mode === "off"
                  ? "Nothing is checked."
                  : rules.mode === "monitor"
                    ? "Failing devices are listed below but keep their access. Use this to see who would be affected."
                    : "Failing devices stay signed in but reach nothing until they pass again, within about 30 seconds of fixing it."
              }
            >
              <Segmented<PostureMode>
                value={rules.mode}
                disabled={!canEdit}
                options={[
                  { value: "off", label: "Off" },
                  { value: "monitor", label: "Report only" },
                  { value: "enforce", label: "Cut off" },
                ]}
                onChange={(mode) => setRules({ ...rules, mode })}
              />
            </PRow>
            <PRow label="Oldest Tailscale version" sub="e.g. 1.70. Older clients miss security fixes.">
              <input
                className="mono input-narrow"
                value={rules.minTailscaleVersion}
                disabled={!canEdit}
                placeholder="Any"
                onChange={(e) => setRules({ ...rules, minTailscaleVersion: e.target.value })}
              />
            </PRow>
            {oses.map((o) => {
              const req = rules.os[o.key] ?? { blocked: false, minVersion: "" };
              return (
                <PRow key={o.key} label={o.label} sub={req.blocked ? "No device running it may connect." : o.hint}>
                  <div className="row">
                    {!req.blocked && o.placeholder && (
                      <input
                        className="mono input-narrow"
                        aria-label={`Oldest ${o.label} version`}
                        value={req.minVersion}
                        disabled={!canEdit}
                        placeholder={`Any (e.g. ${o.placeholder})`}
                        onChange={(e) => setOS(o.key, { minVersion: e.target.value })}
                      />
                    )}
                    <Segmented<"allow" | "block">
                      value={req.blocked ? "block" : "allow"}
                      disabled={!canEdit}
                      options={[
                        { value: "allow", label: "Allowed" },
                        { value: "block", label: "Blocked" },
                      ]}
                      onChange={(v) => setOS(o.key, { blocked: v === "block" })}
                    />
                  </div>
                </PRow>
              );
            })}
            <PRow
              label="Require Intune compliance"
              sub={
                !data.entraReady ? (
                  <>
                    Needs Microsoft Entra ID: set up Entra directory sync or Entra single sign-on under{" "}
                    <Link to="/settings">Settings</Link> first.
                  </>
                ) : data.intuneError ? (
                  <span className="text-warn">{data.intuneError}</span>
                ) : data.intuneAt ? (
                  `${data.intuneDevices} Intune device${data.intuneDevices === 1 ? "" : "s"}, read ${timeAgo(data.intuneAt)}. Devices are matched by serial number and owner.`
                ) : (
                  "Only devices Intune reports compliant may connect. Grant the Entra app the application permission DeviceManagementManagedDevices.Read.All."
                )
              }
            >
              <Toggle
                checked={rules.requireIntune}
                disabled={!canEdit || (!data.entraReady && !rules.requireIntune)}
                label="Require Intune compliance"
                hideLabel
                onChange={(requireIntune) => setRules({ ...rules, requireIntune })}
              />
            </PRow>
          </Group>
          {canEdit && (
            <div className="row-between mt">
              <span />
              <div className="row">
                <Button variant="ghost" disabled={!dirty || busy} onClick={() => setRules(data.rules)}>
                  Discard
                </Button>
                <Button
                  variant="primary"
                  busy={busy}
                  disabled={!dirty || invalid}
                  onClick={() =>
                    rules.mode === "enforce" && data.rules.mode !== "enforce" ? setConfirmEnforce(true) : void save()
                  }
                >
                  Save
                </Button>
              </div>
            </div>
          )}

          {data.rules.mode !== "off" && (
            <Group
              title={
                <div className="row-between">
                  <span>
                    Devices{" "}
                    {failing > 0 && (
                      <Badge tone={data.rules.mode === "enforce" ? "red" : "amber"}>{failing} failing</Badge>
                    )}
                  </span>
                  <span className="muted small">{data.checkedAt && `Checked ${timeAgo(data.checkedAt)}`}</span>
                </div>
              }
              note="Devices report their system and Tailscale version each time they connect. Intune is read every 10 minutes."
            >
              <div className="row group-toolbar">
                <SearchBox value={q} onChange={setQ} placeholder="Search devices or people" />
                <Segmented<"all" | "failing">
                  value={filter}
                  options={[
                    { value: "all", label: "All" },
                    { value: "failing", label: "Failing" },
                  ]}
                  onChange={setFilter}
                />
              </div>
              {devices.length === 0 ? (
                <PRow
                  label={<span className="muted">{filter === "failing" ? "Every device passes." : "No devices."}</span>}
                />
              ) : (
                devices.map((d) => <DeviceRow key={d.id} d={d} />)
              )}
            </Group>
          )}
        </>
      ) : (
        <Empty title="Nothing here" />
      )}
      {confirmEnforce && rules && data && (
        <Confirm
          title="Cut off failing devices?"
          confirmLabel="Enforce requirements"
          danger
          onClose={() => setConfirmEnforce(false)}
          onConfirm={save}
        >
          {data.rules.mode === "monitor" && failing > 0
            ? `${failing} device${failing === 1 ? "" : "s"} failed the last check and will lose access right away, unless the new requirements let them pass.`
            : "Devices that fail lose access right away. Consider Report only first to see who would be affected."}
        </Confirm>
      )}
    </>
  );
}
