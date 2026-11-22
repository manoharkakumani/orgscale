import { useMemo, useState } from "react";
import { get, type AdminDevice, type AdminGroup, type AdminUser, type NetworksInfo, type Principal, type PrincipalKind, type ServerTag } from "../api";
import { Icon, Spinner, useAsync } from "./ui";

// The people, groups, tags and devices an administrator can pick from when
// granting access. Loaded once per picker.

export type Option = Principal & { label: string; sub: string; key: string };

const icons: Record<PrincipalKind, string> = {
  user: "user",
  directory_group: "users",
  policy_group: "users",
  tag: "tag",
  device: "devices",
  everyone: "everyone",
  self: "user",
  resource: "globe",
};

const sections: { kinds: PrincipalKind[]; title: string }[] = [
  { kinds: ["everyone", "self"], title: "Everyone" },
  { kinds: ["directory_group", "policy_group"], title: "Groups" },
  { kinds: ["user"], title: "People" },
  { kinds: ["tag"], title: "Servers (by tag)" },
  { kinds: ["device"], title: "Devices" },
  { kinds: ["resource"], title: "Private resources" },
];

export const keyOf = (p: Principal) => `${p.kind}:${p.principal}`;

export function useDirectory(kinds: PrincipalKind[]) {
  const need = (k: PrincipalKind) => kinds.includes(k);
  const users = useAsync(() => (need("user") ? get<AdminUser[]>("/admin/api/users") : Promise.resolve([])));
  const groups = useAsync(() => (need("directory_group") || need("policy_group") ? get<AdminGroup[]>("/admin/api/groups") : Promise.resolve([])));
  const tags = useAsync(() => (need("tag") ? get<ServerTag[]>("/admin/api/tags") : Promise.resolve([])));
  const devices = useAsync(() => (need("device") ? get<AdminDevice[]>("/admin/api/devices") : Promise.resolve([])));
  const networks = useAsync(() => (need("resource") ? get<NetworksInfo>("/admin/api/networks") : Promise.resolve({ routers: [], resources: [] })));

  const options = useMemo(() => {
    const out: Option[] = [];
    const add = (o: Omit<Option, "key">) => out.push({ ...o, key: keyOf(o) });
    if (need("everyone")) add({ kind: "everyone", principal: "", label: "Everyone", sub: "every person's devices" });
    if (need("self")) add({ kind: "self", principal: "", label: "Their own devices", sub: "each person's own machines" });
    for (const g of groups.data ?? []) {
      const n = `${g.members.length} member${g.members.length === 1 ? "" : "s"}`;
      if (g.source === "policy" && need("policy_group")) add({ kind: "policy_group", principal: g.name, label: g.displayName || `group:${g.name}`, sub: `policy group · ${n}` });
      else if (g.id && need("directory_group")) add({ kind: "directory_group", principal: String(g.id), label: g.displayName || g.name, sub: `${g.source === "console" ? "group" : g.source.toUpperCase() + " group"} · ${n}` });
    }
    for (const u of users.data ?? []) {
      if (!u.disabled) add({ kind: "user", principal: String(u.id), label: u.email || u.name, sub: u.displayName || "person" });
    }
    for (const t of tags.data ?? []) {
      add({ kind: "tag", principal: t.name, label: t.name, sub: t.description || `${t.devices.length} server${t.devices.length === 1 ? "" : "s"}` });
    }
    for (const d of devices.data ?? []) {
      add({ kind: "device", principal: String(d.id), label: d.name, sub: d.owner === "tagged" ? d.tags.join(", ") : d.owner });
    }
    for (const r of networks.data?.resources ?? []) {
      add({ kind: "resource", principal: String(r.id), label: r.name, sub: r.address });
    }
    return out;
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [users.data, groups.data, tags.data, devices.data, networks.data]);

  return { options, loading: users.loading || groups.loading || tags.loading || devices.loading || networks.loading };
}

export function PrincipalChip({ p, onRemove }: { p: Principal; onRemove?: () => void }) {
  return (
    <span className={`chip ${onRemove ? "" : "chip-static"}`}>
      <Icon name={icons[p.kind]} />
      <span className="ellipsis">{p.kind === "everyone" ? "Everyone" : p.kind === "self" ? "Their own devices" : p.label || p.principal}</span>
      {onRemove && (
        <button type="button" aria-label={`Remove ${p.label}`} onClick={onRemove}>
          ×
        </button>
      )}
    </span>
  );
}

export function PrincipalList({ items, empty = "Nobody", max }: { items: Principal[]; empty?: string; max?: number }) {
  if (!items.length) return <span className="muted">{empty}</span>;
  const shown = max ? items.slice(0, max) : items;
  return (
    <div className="chip-row">
      {shown.map((p) => (
        <PrincipalChip key={keyOf(p)} p={p} />
      ))}
      {max && items.length > max && <span className="muted small">+{items.length - max} more</span>}
    </div>
  );
}

// PrincipalPicker shows the chosen principals as chips above a searchable,
// sectioned list of everything that can be chosen.
export function PrincipalPicker({
  kinds,
  value,
  onChange,
  disabled,
  placeholder = "Search people, groups, servers",
  extra = [],
  height,
}: {
  kinds: PrincipalKind[];
  value: Principal[];
  onChange: (v: Principal[]) => void;
  disabled?: boolean;
  placeholder?: string;
  extra?: Option[];
  height?: number;
}) {
  const dir = useDirectory(kinds);
  const [q, setQ] = useState("");
  const options = useMemo(() => {
    const seen = new Set(dir.options.map((o) => o.key));
    return [...dir.options, ...extra.filter((o) => !seen.has(o.key))];
  }, [dir.options, extra]);

  const label = (p: Principal) => p.label || options.find((o) => o.key === keyOf(p))?.label || p.principal;
  const selected = new Set(value.map(keyOf));
  const toggle = (o: Option) => onChange(selected.has(o.key) ? value.filter((v) => keyOf(v) !== o.key) : [...value, { kind: o.kind, principal: o.principal, label: o.label }]);

  const needle = q.trim().toLowerCase();
  const match = (o: Option) => !needle || o.label.toLowerCase().includes(needle) || o.sub.toLowerCase().includes(needle);

  return (
    <div>
      <div className="chips" style={{ minHeight: 34 }}>
        {value.length === 0 && <span className="muted small" style={{ padding: "0 4px" }}>Nothing chosen yet</span>}
        {value.map((p) => (
          <PrincipalChip key={keyOf(p)} p={{ ...p, label: label(p) }} onRemove={disabled ? undefined : () => onChange(value.filter((v) => keyOf(v) !== keyOf(p)))} />
        ))}
      </div>
      {!disabled && (
        <>
          <input className="search mt" type="search" placeholder={placeholder} value={q} onChange={(e) => setQ(e.target.value)} />
          <div className="picker" style={height ? { maxHeight: height } : undefined}>
            {dir.loading ? (
              <Spinner />
            ) : (
              sections.map((s) => {
                const rows = options.filter((o) => s.kinds.includes(o.kind) && kinds.includes(o.kind) && match(o));
                if (!rows.length) return null;
                return (
                  <div key={s.title}>
                    <div className="picker-head">{s.title}</div>
                    {rows.map((o) => (
                      <label key={o.key} className="picker-row">
                        <input type="checkbox" checked={selected.has(o.key)} onChange={() => toggle(o)} />
                        <Icon name={icons[o.kind]} />
                        <span className="grow ellipsis">
                          {o.label} <span className="muted small">· {o.sub}</span>
                        </span>
                      </label>
                    ))}
                  </div>
                );
              })
            )}
            {!dir.loading && !options.some((o) => kinds.includes(o.kind) && match(o)) && <div className="muted pad">Nothing matches.</div>}
          </div>
        </>
      )}
    </div>
  );
}
