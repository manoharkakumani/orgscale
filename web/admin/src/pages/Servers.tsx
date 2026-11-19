import { useEffect, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { del, get, post, put, type EnrollKey, type Principal, type ServerTag } from "../api";
import { useCanEdit } from "../App";
import { PrincipalList, PrincipalPicker, type Option } from "../components/principals";
import { duration, durationChoices } from "./Requests";
import { Badge, Button, ChipInput, CodeBlock, Confirm, Empty, ErrorBox, Field, Icon, Modal, PRow, PageHeader, Segmented, Spinner, Toggle, dateTime, useAsync, useToast } from "../components/ui";

const granteeKinds = ["everyone", "directory_group", "policy_group", "user", "tag"] as const;

const slug = (s: string) =>
  s
    .toLowerCase()
    .replace(/^tag:/, "")
    .replace(/[^a-z0-9-]+/g, "-")
    .replace(/^-+|-+$/g, "")
    .slice(0, 63);

// ---------- tag editor ----------

function TagEditor({ tag, onClose, onSaved }: { tag?: ServerTag; onClose: () => void; onSaved: (name: string) => void }) {
  const toast = useToast();
  const [name, setName] = useState(tag ? tag.name.replace(/^tag:/, "") : "");
  const [description, setDescription] = useState(tag?.description ?? "");
  const [access, setAccess] = useState<Principal[]>(tag?.access ?? []);
  const [reqMax, setReqMax] = useState(tag?.requestMaxMinutes ?? 0);
  const [reqSSH, setReqSSH] = useState<string[]>(tag?.requestSSHUsers ?? []);
  const [busy, setBusy] = useState(false);
  const full = `tag:${slug(name)}`;
  const self: Option = { kind: "tag", principal: full, label: full, sub: "servers in this tag reach each other", key: `tag:${full}` };

  return (
    <Modal
      title={tag ? `Edit ${tag.name}` : "New server tag"}
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
            disabled={!slug(name)}
            onClick={async () => {
              setBusy(true);
              try {
                await put(`/admin/api/tags/${encodeURIComponent(full)}`, {
                  description,
                  access: access.map(({ kind, principal }) => ({ kind, principal })),
                  requests: { maxMinutes: reqMax, sshUsers: reqMax > 0 ? reqSSH : [] },
                });
                toast.ok(tag ? "Tag saved" : `${full} created`);
                onSaved(full);
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            {tag ? "Save" : "Create tag"}
          </Button>
        </>
      }
    >
      <div className="grid-2">
        <Field label="Name" hint={tag ? "A tag's name can't be changed." : <>Saved as <code>{full}</code></>}>
          <input value={name} disabled={!!tag} placeholder="e.g. databases, web, build-agents" autoFocus={!tag} onChange={(e) => setName(e.target.value)} />
        </Field>
        <Field label="Description">
          <input value={description} placeholder="e.g. Production PostgreSQL servers" onChange={(e) => setDescription(e.target.value)} />
        </Field>
      </div>
      <Field label="Who can reach these servers" hint="Applies to every server with this tag, including ones added later. Groups follow your identity provider.">
        <PrincipalPicker kinds={[...granteeKinds]} value={access} onChange={setAccess} extra={[self]} height={240} />
      </Field>
      <div className="group-title">Temporary access on request</div>
      <div className="group">
        <PRow label="People can request access" sub="Anyone signed in can ask for time-limited access with a reason. Another administrator approves; access ends on its own.">
          <Toggle hideLabel label="People can request access" checked={reqMax > 0} onChange={(v) => setReqMax(v ? 240 : 0)} />
        </PRow>
        {reqMax > 0 && (
          <>
            <PRow label="Longest access">
              <select value={reqMax} onChange={(e) => setReqMax(Number(e.target.value))}>
                {durationChoices.filter((d) => d.minutes >= 60).map((d) => (
                  <option key={d.minutes} value={d.minutes}>
                    {d.label}
                  </option>
                ))}
              </select>
            </PRow>
            <PRow block label="SSH accounts" sub="Linux accounts people may also SSH in as with approved access, e.g. ubuntu. Leave empty for network access only.">
              <ChipInput values={reqSSH} onChange={setReqSSH} placeholder="ubuntu" />
            </PRow>
          </>
        )}
      </div>
    </Modal>
  );
}

// ---------- add server wizard ----------

type Platform = "linux" | "docker" | "windows" | "macos";

function commands(platform: Platform, k: EnrollKey, ssh: boolean): string {
  const url = k.serverUrl;
  const sshFlag = ssh ? " --ssh" : "";
  switch (platform) {
    case "linux":
      return `curl -fsSL https://tailscale.com/install.sh | sh\nsudo tailscale up --login-server=${url} --auth-key=${k.key}${sshFlag}`;
    case "docker":
      return [
        "docker run -d --name=tailscale --restart=unless-stopped \\",
        "  --cap-add=NET_ADMIN --device=/dev/net/tun \\",
        ...(k.ephemeral ? [] : ["  -v tailscale-state:/var/lib/tailscale -e TS_STATE_DIR=/var/lib/tailscale \\"]),
        `  -e TS_AUTHKEY=${k.key} \\`,
        `  -e TS_EXTRA_ARGS="--login-server=${url}${sshFlag}" \\`,
        "  tailscale/tailscale:stable",
      ].join("\n");
    case "windows":
      return `# Install Tailscale from https://tailscale.com/download/windows, then in an admin PowerShell:\n& "C:\\Program Files\\Tailscale\\tailscale.exe" up --login-server=${url} --auth-key=${k.key} --unattended`;
    case "macos":
      return `brew install tailscale\nsudo brew services start tailscale\nsudo tailscale up --login-server=${url} --auth-key=${k.key}${sshFlag}`;
  }
}

const kinds = [
  { id: "one", title: "One server", text: "The key works once.", reusable: false, ephemeral: false },
  { id: "many", title: "Several servers", text: "Reuse the key until it expires.", reusable: true, ephemeral: false },
  { id: "containers", title: "Containers / autoscaling", text: "Servers vanish when they go offline.", reusable: true, ephemeral: true },
] as const;

const validity = [
  { hours: 1, label: "1 hour" },
  { hours: 24, label: "1 day" },
  { hours: 24 * 7, label: "7 days" },
  { hours: 24 * 30, label: "30 days" },
  { hours: 24 * 90, label: "90 days" },
  { hours: 24 * 180, label: "180 days" },
  { hours: 24 * 360, label: "360 days" },
];

function AddServer({ tags, initialTag, onClose, onChanged }: { tags: ServerTag[]; initialTag?: string; onClose: () => void; onChanged: () => void }) {
  const toast = useToast();
  const [tag, setTag] = useState(initialTag ?? tags[0]?.name ?? "");
  // A new tag can be named right here; it is created with the install key.
  const [newTag, setNewTag] = useState(tags.length ? "" : "servers");
  const [isNew, setIsNew] = useState(tags.length === 0);
  const [created, setCreated] = useState(false);
  const target = isNew ? `tag:${slug(newTag)}` : tag;
  const [kind, setKind] = useState<(typeof kinds)[number]["id"]>("one");
  const [hours, setHours] = useState(24);
  const [key, setKey] = useState<EnrollKey | null>(null);
  const [platform, setPlatform] = useState<Platform>("linux");
  const [ssh, setSSH] = useState(true);
  const [busy, setBusy] = useState(false);
  const [joined, setJoined] = useState<string[]>([]);
  const before = tags.find((t) => t.name === tag)?.devices.map((d) => d.id) ?? [];

  // Watch for servers joining once the key exists.
  useEffect(() => {
    if (!key) return;
    const known = new Set(before);
    const timer = setInterval(async () => {
      try {
        const now = await get<ServerTag[]>("/admin/api/tags");
        const t = now.find((x) => x.name === key.tag);
        const fresh = (t?.devices ?? []).filter((d) => !known.has(d.id)).map((d) => d.name);
        if (fresh.length) {
          setJoined(fresh);
          onChanged();
        }
      } catch {
        /* keep polling */
      }
    }, 3000);
    return () => clearInterval(timer);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key]);

  const create = async () => {
    const k = kinds.find((x) => x.id === kind)!;
    setBusy(true);
    try {
      if (isNew) {
        await put(`/admin/api/tags/${encodeURIComponent(target)}`, { description: "", access: [] });
        setCreated(true);
        onChanged();
      }
      setKey(await post<EnrollKey>(`/admin/api/tags/${encodeURIComponent(target)}/enroll`, { reusable: k.reusable, ephemeral: k.ephemeral, expiresInHours: hours }));
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <Modal
      title="Add a server"
      onClose={onClose}
      wide
      footer={
        key ? (
          <Button variant="primary" onClick={onClose}>
            Done
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" busy={busy} disabled={isNew ? !slug(newTag) : !tag} onClick={create}>
              Create install command
            </Button>
          </>
        )
      }
    >
      <div className="wizard-steps">
        <span className="on" />
        <span className={key ? "on" : ""} />
      </div>
      {!key ? (
        <>
          <Field label="Group of servers (tag)" hint={isNew ? undefined : "The server belongs to this tag, not to a person. The tag's access list decides who can reach it."}>
            <div className="choice-grid">
              {tags.map((t) => (
                <button
                  key={t.name}
                  type="button"
                  className={`choice ${!isNew && tag === t.name ? "active" : ""}`}
                  onClick={() => {
                    setIsNew(false);
                    setTag(t.name);
                  }}
                >
                  <b>{t.name}</b>
                  <span className="muted small">{t.description || `${t.devices.length} server${t.devices.length === 1 ? "" : "s"}`}</span>
                </button>
              ))}
              <button type="button" className={`choice ${isNew ? "active" : ""}`} onClick={() => setIsNew(true)}>
                <b>+ New</b>
                <span className="muted small">Name a new group</span>
              </button>
            </div>
            {isNew && (
              <div className="mt">
                <input value={newTag} autoFocus placeholder="e.g. databases, web, build-agents" onChange={(e) => setNewTag(e.target.value)} />
                <div className="field-hint">
                  Saved as <code>tag:{slug(newTag) || "…"}</code>. Servers in it belong to the tag, not a person; you choose who can reach them afterwards.
                </div>
              </div>
            )}
          </Field>
          <Field label="What are you adding?">
            <div className="choice-grid">
              {kinds.map((k) => (
                <button key={k.id} type="button" className={`choice ${kind === k.id ? "active" : ""}`} onClick={() => setKind(k.id)}>
                  <b>{k.title}</b>
                  <span className="muted small">{k.text}</span>
                </button>
              ))}
            </div>
          </Field>
          <Field label="Install command valid for" hint="Servers that already joined keep working after this.">
            <select value={String(hours)} onChange={(e) => setHours(Number(e.target.value))} style={{ maxWidth: 220 }}>
              {validity.map((v) => (
                <option key={v.hours} value={v.hours}>
                  {v.label}
                </option>
              ))}
            </select>
          </Field>
        </>
      ) : (
        <>
          <div style={{ marginBottom: 12 }}>
            <Segmented
              value={platform}
              options={[
                { value: "linux", label: "Linux" },
                { value: "docker", label: "Docker" },
                { value: "windows", label: "Windows" },
                { value: "macos", label: "macOS" },
              ]}
              onChange={setPlatform}
            />
            <div className="muted small mt-xs">
              {key.reusable ? "Reusable" : "Single use"}
              {key.ephemeral && ", ephemeral"} · valid until {dateTime(key.expiration)}
            </div>
          </div>
          <p>Run this on the server{key.reusable ? "s" : ""}. It installs Tailscale and joins the network as <b>{key.tag}</b>:</p>
          <CodeBlock code={commands(platform, key, ssh && platform !== "windows")} />
          {platform !== "windows" && (
            <div className="group mt">
              <PRow label="Allow SSH through Tailscale" sub="Who may SSH in, and as which account, is set with SSH rules on Access. No SSH keys needed.">
                <Toggle checked={ssh} onChange={setSSH} label="Allow SSH through Tailscale" hideLabel />
              </PRow>
            </div>
          )}
          <div className={`callout mt ${joined.length ? "callout-ok" : "callout-info"}`}>
            {joined.length ? (
              <span>
                <b>{joined.join(", ")}</b> joined the network.
              </span>
            ) : (
              <span className="row" style={{ margin: 0 }}>
                <span className="spinner spinner-sm" /> Waiting for the server to connect… you can close this; it will appear here.
              </span>
            )}
          </div>
          {created && (
            <div className="callout callout-warn">
              <span>
                Nobody can reach <b>{key.tag}</b> yet. Close this and use <b>Edit access</b> on its card to choose who can.
              </span>
            </div>
          )}
          <p className="muted small">The key is shown only now. Treat it like a password.</p>
        </>
      )}
    </Modal>
  );
}

// ---------- page ----------

function TagCard({ t, canEdit, onAdd, onEdit, onDelete }: { t: ServerTag; canEdit: boolean; onAdd: () => void; onEdit: () => void; onDelete: () => void }) {
  const online = t.devices.filter((d) => d.online).length;
  return (
    <div className="card tag-card">
      <div className="tag-card-head">
        <div className="tag-icon">
          <Icon name="server" />
        </div>
        <div className="grow">
          <h2 style={{ margin: 0 }}>{t.name}</h2>
          <div className="muted small">{t.description || (t.managed ? "No description" : "Defined in the policy file")}</div>
        </div>
        <Badge tone={online ? "green" : "neutral"}>
          {online}/{t.devices.length} online
        </Badge>
      </div>
      <div>
        <div className="muted small mb-xs">Servers</div>
        {t.devices.length ? (
          <div className="chip-row">
            {t.devices.slice(0, 6).map((d) => (
              <span key={d.id} className="chip chip-static">
                <span className={`dot ${d.online ? "dot-on" : ""}`} /> {d.name}
              </span>
            ))}
            {t.devices.length > 6 && <span className="muted small">+{t.devices.length - 6} more</span>}
          </div>
        ) : (
          <span className="muted">None yet</span>
        )}
      </div>
      <div>
        <div className="muted small mb-xs">Who can reach them</div>
        <PrincipalList items={t.access} empty="Nobody yet" max={5} />
        {t.requestMaxMinutes > 0 && (
          <div className="muted small mt">
            Others can request access for up to {duration(t.requestMaxMinutes)}
            {t.requestSSHUsers.length ? `, with SSH as ${t.requestSSHUsers.join(", ")}` : ""}.
          </div>
        )}
      </div>
      {canEdit && (
        <div className="tag-card-foot">
          <Button variant="primary" onClick={onAdd}>
            <Icon name="plus" /> Add server
          </Button>
          {t.managed && <Button onClick={onEdit}>Edit access</Button>}
          <span className="grow" />
          {t.managed && (
            <button className="icon-btn" aria-label={`Delete ${t.name}`} onClick={onDelete}>
              <Icon name="trash" />
            </button>
          )}
        </div>
      )}
    </div>
  );
}

export default function ServersPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<ServerTag[]>("/admin/api/tags"));
  const [editing, setEditing] = useState<ServerTag | "new" | null>(null);
  const [params, setParams] = useSearchParams();
  const [adding, setAdding] = useState<{ tag?: string } | null>(params.get("add") ? {} : null);
  const [deleting, setDeleting] = useState<ServerTag | null>(null);

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  const tags = data ?? [];

  return (
    <>
      <PageHeader
        title="Servers"
        description={
          <>
            Servers, VMs and containers join with an install command instead of a person's sign-in. Each belongs to a tag, and the tag decides who can reach it. A machine someone already set up with their own account can be turned into a server from its page on <Link to="/devices">Devices</Link>.
          </>
        }
        actions={
          canEdit && (
            <>
              <Button onClick={() => setEditing("new")}>New tag</Button>
              <Button variant="primary" onClick={() => setAdding({})}>
                <Icon name="plus" /> Add server
              </Button>
            </>
          )
        }
      />

      {tags.length === 0 ? (
        <Empty title="Group your servers with tags">
          Create a tag such as <code>tag:databases</code>, choose who can reach it, then add servers to it with a one-line install command.
          {canEdit && (
            <div>
              <Button variant="primary" onClick={() => setEditing("new")}>
                <Icon name="plus" /> New tag
              </Button>
            </div>
          )}
        </Empty>
      ) : (
        <div className="tag-grid">
          {tags.map((t) => (
            <TagCard key={t.name} t={t} canEdit={canEdit} onAdd={() => setAdding({ tag: t.name })} onEdit={() => setEditing(t)} onDelete={() => setDeleting(t)} />
          ))}
        </div>
      )}

      {editing && (
        <TagEditor
          tag={editing === "new" ? undefined : editing}
          onClose={() => setEditing(null)}
          onSaved={(name) => {
            void reload();
            if (adding) setAdding({ tag: name });
          }}
        />
      )}
      {adding && !editing && <AddServer tags={tags} initialTag={adding.tag} onClose={() => { setAdding(null); if (params.get("add")) setParams({}); }} onChanged={() => void reload()} />}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete tag"
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await del(`/admin/api/tags/${encodeURIComponent(deleting.name)}`);
            toast.ok("Tag deleted");
            void reload();
          }}
        >
          {deleting.devices.length ? `${deleting.devices.length} server(s) still use this tag; move them to another tag first.` : "The tag and its access list are removed. Unused install keys for it stop working."}
        </Confirm>
      )}
    </>
  );
}
