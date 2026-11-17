import { useState } from "react";
import { get, post, type AdminUser, type ApiKey, type PreAuthKey } from "../api";
import { useCanEdit } from "../App";
import { Badge, Button, ChipInput, CopyText, Empty, ErrorBox, Field, Modal, PageHeader, Spinner, dateTime, timeAgo, useAsync, useToast } from "../components/ui";

function expiryIn(days: number): string {
  return new Date(Date.now() + days * 86400_000).toISOString();
}

function isExpired(iso: string | null): boolean {
  return !!iso && new Date(iso).getTime() < Date.now();
}

function NewAuthKey({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const users = useAsync(() => get<AdminUser[]>("/admin/api/users"));
  const toast = useToast();
  const [owner, setOwner] = useState<"tags" | string>("tags");
  const [tags, setTags] = useState<string[]>([]);
  const [reusable, setReusable] = useState(false);
  const [ephemeral, setEphemeral] = useState(false);
  const [days, setDays] = useState(7);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState("");

  if (created)
    return (
      <Modal title="Auth key created" onClose={onClose} footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
        <p>Copy the key now: it is not shown again.</p>
        <CopyText value={created} />
        <p className="muted small mt">
          On the machine: <code>tailscale up --login-server {window.location.origin} --auth-key {"<key>"}</code>
        </p>
      </Modal>
    );

  return (
    <Modal
      title="New auth key"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={busy}
            disabled={owner === "tags" && tags.length === 0}
            onClick={async () => {
              setBusy(true);
              try {
                const res = await post<{ preAuthKey: PreAuthKey }>("/api/v1/preauthkey", {
                  user: owner === "tags" ? undefined : owner,
                  reusable,
                  ephemeral,
                  expiration: expiryIn(days),
                  aclTags: owner === "tags" ? tags : tags.length ? tags : undefined,
                });
                setCreated(res.preAuthKey.key);
                onDone();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Create key
          </Button>
        </>
      }
    >
      <p className="muted">Auth keys add machines without an interactive sign-in: servers, containers, CI runners.</p>
      <Field label="Devices belong to" hint="Servers usually belong to tags; tags must be owned in the policy (tagOwners).">
        <select value={owner} onChange={(e) => setOwner(e.target.value)}>
          <option value="tags">Tags (recommended for servers)</option>
          {(users.data ?? [])
            .filter((u) => !u.disabled)
            .map((u) => (
              <option key={u.id} value={String(u.id)}>
                {u.email || u.name}
              </option>
            ))}
        </select>
      </Field>
      <Field label="Tags">
        <ChipInput values={tags} onChange={setTags} placeholder="tag:server" validate={(v) => (v.startsWith("tag:") ? null : "Tags start with tag:")} />
      </Field>
      <div className="check-row">
        <label>
          <input type="checkbox" checked={reusable} onChange={(e) => setReusable(e.target.checked)} /> Reusable (for many machines)
        </label>
        <label>
          <input type="checkbox" checked={ephemeral} onChange={(e) => setEphemeral(e.target.checked)} /> Ephemeral (device removed when it goes offline)
        </label>
      </div>
      <Field label="Expires in">
        <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {[1, 7, 30, 90].map((d) => (
            <option key={d} value={d}>
              {d} day{d === 1 ? "" : "s"}
            </option>
          ))}
        </select>
      </Field>
    </Modal>
  );
}

function NewApiKey({ onClose, onDone }: { onClose: () => void; onDone: () => void }) {
  const toast = useToast();
  const [days, setDays] = useState(90);
  const [busy, setBusy] = useState(false);
  const [created, setCreated] = useState("");

  if (created)
    return (
      <Modal title="API key created" onClose={onClose} footer={<Button variant="primary" onClick={onClose}>Done</Button>}>
        <p>Copy the key now: it is not shown again. It has full admin access.</p>
        <CopyText value={created} />
      </Modal>
    );

  return (
    <Modal
      title="New API key"
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={busy}
            onClick={async () => {
              setBusy(true);
              try {
                const res = await post<{ apiKey: string }>("/api/v1/apikey", { expiration: expiryIn(days) });
                setCreated(res.apiKey);
                onDone();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            Create key
          </Button>
        </>
      }
    >
      <p className="muted">API keys are for scripts, Terraform and the orgscale CLI on another machine. They have full admin access.</p>
      <Field label="Expires in">
        <select value={days} onChange={(e) => setDays(Number(e.target.value))}>
          {[7, 30, 90, 365].map((d) => (
            <option key={d} value={d}>
              {d} days
            </option>
          ))}
        </select>
      </Field>
    </Modal>
  );
}

export default function KeysPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const auth = useAsync(() => get<{ preAuthKeys: PreAuthKey[] }>("/api/v1/preauthkey"));
  const api = useAsync(() => get<{ apiKeys: ApiKey[] }>("/api/v1/apikey"));
  const [modal, setModal] = useState<"auth" | "api" | null>(null);
  const [showExpired, setShowExpired] = useState(false);

  const authKeys = (auth.data?.preAuthKeys ?? []).filter((k) => showExpired || !isExpired(k.expiration)).sort((a, b) => b.createdAt.localeCompare(a.createdAt));
  const apiKeys = (api.data?.apiKeys ?? []).filter((k) => showExpired || !isExpired(k.expiration));

  const expireAuth = async (k: PreAuthKey) => {
    try {
      await post("/api/v1/preauthkey/expire", { id: k.id });
      toast.ok("Auth key expired");
      void auth.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  const expireApi = async (k: ApiKey) => {
    try {
      await post("/api/v1/apikey/expire", { prefix: k.prefix });
      toast.ok("API key expired");
      void api.reload();
    } catch (e) {
      toast.error(e);
    }
  };

  return (
    <>
      <PageHeader title="Keys" description="Auth keys add machines without a sign-in. API keys let scripts and Terraform manage orgscale." actions={<label className="small"><input type="checkbox" checked={showExpired} onChange={(e) => setShowExpired(e.target.checked)} /> Show expired</label>} />

      <section className="card section">
        <div className="section-head">
          <h2>Auth keys</h2>
          {canEdit && (
            <Button variant="primary" onClick={() => setModal("auth")}>
              New auth key
            </Button>
          )}
        </div>
        {auth.error && <ErrorBox error={auth.error} onRetry={auth.reload} />}
        {auth.loading && !auth.data ? (
          <Spinner />
        ) : authKeys.length === 0 ? (
          <Empty title="No auth keys" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Key</th>
                  <th>Belongs to</th>
                  <th>Type</th>
                  <th>Created</th>
                  <th>Expires</th>
                  {canEdit && <th aria-label="Actions" />}
                </tr>
              </thead>
              <tbody>
                {authKeys.map((k) => (
                  <tr key={k.id} className={isExpired(k.expiration) ? "row-muted" : ""}>
                    <td className="mono small">{k.key}</td>
                    <td>{k.aclTags.length ? k.aclTags.join(", ") : k.user?.email || k.user?.name || "—"}</td>
                    <td>
                      <div className="chip-row">
                        {k.reusable ? <Badge>reusable</Badge> : <Badge>{k.used ? "used" : "one-time"}</Badge>}
                        {k.ephemeral && <Badge tone="amber">ephemeral</Badge>}
                      </div>
                    </td>
                    <td className="muted">{timeAgo(k.createdAt)}</td>
                    <td className="muted" title={dateTime(k.expiration)}>
                      {isExpired(k.expiration) ? <Badge tone="red">expired</Badge> : timeAgo(k.expiration)}
                    </td>
                    {canEdit && <td className="actions">{!isExpired(k.expiration) && <Button variant="ghost" onClick={() => expireAuth(k)}>Expire</Button>}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="card section">
        <div className="section-head">
          <h2>API keys</h2>
          {canEdit && (
            <Button variant="primary" onClick={() => setModal("api")}>
              New API key
            </Button>
          )}
        </div>
        {api.error && <ErrorBox error={api.error} onRetry={api.reload} />}
        {api.loading && !api.data ? (
          <Spinner />
        ) : apiKeys.length === 0 ? (
          <Empty title="No API keys" />
        ) : (
          <div className="table-wrap">
            <table>
              <thead>
                <tr>
                  <th>Key</th>
                  <th>Created</th>
                  <th>Last used</th>
                  <th>Expires</th>
                  {canEdit && <th aria-label="Actions" />}
                </tr>
              </thead>
              <tbody>
                {apiKeys.map((k) => (
                  <tr key={k.id} className={isExpired(k.expiration) ? "row-muted" : ""}>
                    <td className="mono small">{k.prefix}</td>
                    <td className="muted">{timeAgo(k.createdAt)}</td>
                    <td className="muted">{timeAgo(k.lastSeen)}</td>
                    <td className="muted">{isExpired(k.expiration) ? <Badge tone="red">expired</Badge> : timeAgo(k.expiration)}</td>
                    {canEdit && <td className="actions">{!isExpired(k.expiration) && <Button variant="ghost" onClick={() => expireApi(k)}>Expire</Button>}</td>}
                  </tr>
                ))}
              </tbody>
            </table>
          </div>
        )}
      </section>

      {modal === "auth" && <NewAuthKey onClose={() => setModal(null)} onDone={auth.reload} />}
      {modal === "api" && <NewApiKey onClose={() => setModal(null)} onDone={api.reload} />}
    </>
  );
}
