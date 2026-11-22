import { useEffect, useState } from "react";
import { get, put, type TailnetLink } from "../api";
import { useCanEdit } from "../App";
import { Button, ErrorBox, Group, Icon, PRow, Spinner, useAsync, useToast } from "./ui";

const urlRE = /^https:\/\/[^\s/]+/;

// TailnetLinks edits the list of other orgscale tailnets: each is a separate
// orgscale server with its own database, linked here for quick switching.
export default function TailnetLinks() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<TailnetLink[]>("/admin/api/tailnets"));
  const [rows, setRows] = useState<TailnetLink[] | null>(null);
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    if (data) setRows(data);
  }, [data]);

  if (loading && !rows) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!rows || !data) return null;

  const dirty = JSON.stringify(rows) !== JSON.stringify(data);
  const valid = rows.every(
    (r) => (r.name.trim() === "" && r.url.trim() === "") || (r.name.trim() !== "" && urlRE.test(r.url.trim())),
  );

  return (
    <>
      <Group
        title="Other tailnets"
        note="Each tailnet is its own orgscale server with its own database, devices and rules; nothing is shared between them. Links listed here appear in the sidebar to switch between their consoles."
      >
        {rows.length === 0 && <PRow label={<span className="muted">No other tailnets linked.</span>} />}
        {rows.map((r, i) => (
          <div className="prow" key={i}>
            <div className="grid-2 grow">
              <input
                aria-label="Tailnet name"
                value={r.name}
                disabled={!canEdit}
                placeholder="e.g. Staging"
                onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, name: e.target.value } : x)))}
              />
              <input
                aria-label="Console address"
                className="mono"
                value={r.url}
                disabled={!canEdit}
                placeholder="https://staging-vpn.example.com/admin"
                onChange={(e) => setRows(rows.map((x, j) => (j === i ? { ...x, url: e.target.value } : x)))}
              />
            </div>
            {canEdit && (
              <button
                className="icon-btn"
                aria-label={`Remove ${r.name || "tailnet"}`}
                onClick={() => setRows(rows.filter((_, j) => j !== i))}
              >
                <Icon name="trash" />
              </button>
            )}
          </div>
        ))}
        {canEdit && (
          <PRow
            label={
              <Button variant="ghost" onClick={() => setRows([...rows, { name: "", url: "" }])}>
                <Icon name="plus" /> Link a tailnet
              </Button>
            }
          />
        )}
      </Group>
      {canEdit && (
        <div className="row-between mt">
          <span />
          <div className="row">
            {dirty && (
              <Button variant="ghost" onClick={() => setRows(data)}>
                Discard
              </Button>
            )}
            <Button
              variant="primary"
              busy={busy}
              disabled={!dirty || !valid}
              onClick={async () => {
                setBusy(true);
                try {
                  const saved = await put<TailnetLink[]>("/admin/api/tailnets", rows);
                  setRows(saved);
                  void reload();
                  window.dispatchEvent(new Event("orgscale:tailnets"));
                  toast.ok("Tailnets saved");
                } catch (e) {
                  toast.error(e);
                } finally {
                  setBusy(false);
                }
              }}
            >
              Save
            </Button>
          </div>
        </div>
      )}
    </>
  );
}

// TailnetSwitcher lists the linked tailnets in the sidebar.
export function TailnetSwitcher() {
  const [links, setLinks] = useState<TailnetLink[]>([]);

  useEffect(() => {
    const load = () =>
      get<TailnetLink[]>("/admin/api/tailnets")
        .then(setLinks)
        .catch(() => setLinks([]));
    void load();
    window.addEventListener("orgscale:tailnets", load);
    return () => window.removeEventListener("orgscale:tailnets", load);
  }, []);

  if (links.length === 0) return null;

  return (
    <select
      className="tailnet-switch"
      aria-label="Switch tailnet"
      value=""
      onChange={(e) => {
        if (e.target.value) window.location.href = e.target.value;
      }}
    >
      <option value="">This tailnet ({window.location.host})</option>
      {links.map((l) => (
        <option key={l.url} value={l.url}>
          {l.name}
        </option>
      ))}
    </select>
  );
}
