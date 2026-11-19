import { useEffect, useState } from "react";
import { del, get, put, type DNSRecordInfo, type DNSSettings } from "../api";
import { useCanEdit } from "../App";
import { Badge, Button, ChipInput, Confirm, ErrorBox, Group, Icon, PRow, Spinner, Toggle, useAsync, useToast } from "./ui";

const ipRE = /^(\d{1,3}(\.\d{1,3}){3}|[0-9a-fA-F:]*:[0-9a-fA-F:.]*)$/;
const domainRE = /^([a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)(\.[a-z0-9]([a-z0-9-]{0,61}[a-z0-9])?)*$/;

const validServer = (v: string) => (ipRE.test(v) || /^https:\/\/[^\s/]+/.test(v) ? null : "Use an IP address, or an https:// DNS-over-HTTPS address");
const validDomain = (v: string) => (domainRE.test(v) ? null : "Not a domain, e.g. corp.example.com");
const lowerDomain = (v: string) => v.trim().toLowerCase().replace(/^\.+|\.+$/g, "");

type Split = { domain: string; servers: string[] };

export default function DNSEditor() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload, setData } = useAsync(() => get<DNSSettings>("/admin/api/settings/dns"));
  const [form, setForm] = useState<DNSSettings | null>(null);
  const [splits, setSplits] = useState<Split[]>([]);
  const [busy, setBusy] = useState(false);
  const [resetting, setResetting] = useState(false);

  useEffect(() => {
    if (!data) return;
    setForm(data);
    setSplits(Object.entries(data.splitDNS).map(([domain, servers]) => ({ domain, servers })));
  }, [data]);

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!form || !data) return null;

  const set = (p: Partial<DNSSettings>) => setForm({ ...form, ...p });
  const setRecord = (i: number, p: Partial<DNSRecordInfo>) => set({ records: form.records.map((r, j) => (j === i ? { ...r, ...p } : r)) });
  const setSplit = (i: number, p: Partial<Split>) => setSplits(splits.map((s, j) => (j === i ? { ...s, ...p } : s)));

  const body = () => ({
    magicDNS: form.magicDNS,
    overrideLocalDNS: form.overrideLocalDNS && form.nameservers.length > 0,
    nameservers: form.nameservers,
    splitDNS: Object.fromEntries(splits.filter((s) => s.domain.trim()).map((s) => [lowerDomain(s.domain), s.servers])),
    searchDomains: form.searchDomains,
    records: form.recordsFromFile ? [] : form.records.filter((r) => r.name.trim() || r.value.trim()).map((r) => ({ name: r.name.trim(), type: "", value: r.value.trim() })),
  });

  const dirty =
    JSON.stringify(body()) !==
    JSON.stringify({
      magicDNS: data.magicDNS,
      overrideLocalDNS: data.overrideLocalDNS,
      nameservers: data.nameservers,
      splitDNS: data.splitDNS,
      searchDomains: data.searchDomains,
      records: data.recordsFromFile ? [] : data.records.map((r) => ({ name: r.name, type: "", value: r.value })),
    });

  const save = async () => {
    setBusy(true);
    try {
      const next = await put<DNSSettings>("/admin/api/settings/dns", body());
      setData(next);
      toast.ok("DNS saved. Devices pick it up within seconds.");
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const example = form.baseDomain || "tailnet.example.com";

  return (
    <>
      {data.source === "file" ? (
        <p className="muted small">Showing the config file's DNS settings. Saving here overrides them; you can go back to the file at any time.</p>
      ) : (
        <p className="muted small">
          <Badge tone="blue">Set in the console</Badge> These override the DNS settings in the config file.
        </p>
      )}

      <Group title="Device names">
        <PRow
          label="MagicDNS"
          sub={
            form.baseDomain ? (
              <>
                Devices reach each other by name, e.g. <code>db-server</code> or <code>db-server.{form.baseDomain}</code>.
              </>
            ) : (
              <>Set dns.base_domain in the config file to use MagicDNS.</>
            )
          }
        >
          <Toggle hideLabel label="MagicDNS" checked={form.magicDNS} disabled={!canEdit || !form.baseDomain} onChange={(v) => set({ magicDNS: v })} />
        </PRow>
        <PRow label="Name domain" sub="Set in the config file (dns.base_domain); changing it renames every device.">
          <code>{form.baseDomain || "not set"}</code>
        </PRow>
      </Group>

      <Group title="DNS servers" note="IP addresses, or https:// DNS-over-HTTPS addresses such as https://dns.google/dns-query.">
        <PRow block label="Servers for everything else" sub={form.nameservers.length === 0 ? "None: devices use their own DNS." : form.overrideLocalDNS ? "Devices use these instead of their own DNS." : "Devices keep their own DNS and use these only as a fallback."}>
          <ChipInput values={form.nameservers} onChange={(v) => set({ nameservers: v })} placeholder="1.1.1.1" validate={validServer} disabled={!canEdit} />
        </PRow>
        <PRow label="Use instead of devices' own DNS" sub={form.nameservers.length === 0 ? "Add a server first." : "Turn on to filter or log DNS centrally, e.g. through your company resolver."}>
          <Toggle hideLabel label="Use instead of devices' own DNS" checked={form.overrideLocalDNS && form.nameservers.length > 0} disabled={!canEdit || form.nameservers.length === 0} onChange={(v) => set({ overrideLocalDNS: v })} />
        </PRow>
      </Group>

      <Group title="Split DNS" note="Send lookups for internal domains to your own DNS servers, e.g. corp.example.com to the domain controller. Everything else is unaffected.">
        {splits.length === 0 && <PRow label={<span className="muted">No internal domains</span>} />}
        {splits.map((s, i) => (
          <div className="prow dns-split" key={i}>
            <input value={s.domain} placeholder="corp.example.com" disabled={!canEdit} onChange={(e) => setSplit(i, { domain: e.target.value })} aria-label="Domain" />
            <ChipInput values={s.servers} onChange={(v) => setSplit(i, { servers: v })} placeholder="10.0.0.53" validate={validServer} disabled={!canEdit} />
            {canEdit && (
              <button className="icon-btn" aria-label={`Remove ${s.domain || "domain"}`} onClick={() => setSplits(splits.filter((_, j) => j !== i))}>
                <Icon name="trash" />
              </button>
            )}
          </div>
        ))}
        {canEdit && (
          <PRow label={<Button variant="ghost" onClick={() => setSplits([...splits, { domain: "", servers: [] }])}><Icon name="plus" /> Add internal domain</Button>} />
        )}
      </Group>

      <Group title="Search domains" note="Lets people type short names, e.g. intranet instead of intranet.corp.example.com.">
        <PRow block label="Domains">
          <ChipInput values={form.searchDomains} onChange={(v) => set({ searchDomains: v })} placeholder="corp.example.com" validate={validDomain} normalize={lowerDomain} disabled={!canEdit} />
        </PRow>
      </Group>

      <Group
        title="Custom records"
        note={form.recordsFromFile ? "Managed by the records file in the config (dns.extra_records_path); edit that file to change them." : `Names devices resolve to an address, e.g. grafana.${example} → 100.64.0.9.`}
      >
        {form.records.length === 0 && <PRow label={<span className="muted">No records</span>} />}
        {form.records.map((r, i) => (
          <div className="prow dns-record" key={i}>
            <input value={r.name} placeholder={`grafana.${example}`} disabled={!canEdit || form.recordsFromFile} onChange={(e) => setRecord(i, { name: e.target.value })} aria-label="Name" />
            <span className="muted">→</span>
            <input value={r.value} placeholder="100.64.0.9" disabled={!canEdit || form.recordsFromFile} onChange={(e) => setRecord(i, { value: e.target.value })} aria-label="Address" />
            {canEdit && !form.recordsFromFile && (
              <button className="icon-btn" aria-label={`Remove ${r.name || "record"}`} onClick={() => set({ records: form.records.filter((_, j) => j !== i) })}>
                <Icon name="trash" />
              </button>
            )}
          </div>
        ))}
        {canEdit && !form.recordsFromFile && (
          <PRow label={<Button variant="ghost" onClick={() => set({ records: [...form.records, { name: "", type: "", value: "" }] })}><Icon name="plus" /> Add record</Button>} />
        )}
      </Group>

      {canEdit && (
        <div className="row-between mt">
          {data.source === "console" ? (
            <Button variant="ghost" onClick={() => setResetting(true)}>
              Use the config file's settings
            </Button>
          ) : (
            <span />
          )}
          <div className="row">
            {dirty && (
              <Button variant="ghost" onClick={() => setData({ ...data })}>
                Discard
              </Button>
            )}
            <Button variant="primary" busy={busy} disabled={!dirty} onClick={save}>
              Save
            </Button>
          </div>
        </div>
      )}

      {resetting && (
        <Confirm
          title="Go back to the config file's DNS?"
          confirmLabel="Use config file"
          onClose={() => setResetting(false)}
          onConfirm={async () => {
            const next = await del<DNSSettings>("/admin/api/settings/dns");
            if (next) setData(next);
            else void reload();
            toast.ok("Using the config file's DNS settings");
          }}
        >
          The DNS settings made here are removed, and devices get the ones in the config file again.
        </Confirm>
      )}
    </>
  );
}
