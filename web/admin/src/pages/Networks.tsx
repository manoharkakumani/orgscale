import { useEffect, useState } from "react";
import { del, get, post, put, type EnrollKey, type NetworksInfo, type PrivateResource, type ServerTag, type SubnetRouter } from "../api";
import { useCanEdit } from "../App";
import { Badge, Button, CodeBlock, Confirm, Empty, ErrorBox, Field, Group, Icon, Modal, PRow, PageHeader, Spinner, useAsync, useToast } from "../components/ui";

const cidrRE = /^(\d{1,3}(\.\d{1,3}){3}|[0-9a-fA-F:]*:[0-9a-fA-F:.]*)(\/\d{1,3})?$/;

function ResourceEditor({ resource, onClose, onSaved }: { resource?: PrivateResource; onClose: () => void; onSaved: () => void }) {
  const toast = useToast();
  const [name, setName] = useState(resource?.name ?? "");
  const [address, setAddress] = useState(resource?.address.replace(/\/(32|128)$/, "") ?? "");
  const [description, setDescription] = useState(resource?.description ?? "");
  const [busy, setBusy] = useState(false);

  return (
    <Modal
      title={resource ? `Edit ${resource.name}` : "Add a private resource"}
      onClose={onClose}
      footer={
        <>
          <Button variant="ghost" onClick={onClose}>
            Cancel
          </Button>
          <Button
            variant="primary"
            busy={busy}
            disabled={!name.trim() || !cidrRE.test(address.trim())}
            onClick={async () => {
              setBusy(true);
              try {
                const body = { name, address, description };
                if (resource) await put(`/admin/api/resources/${resource.id}`, body);
                else await post("/admin/api/resources", body);
                toast.ok(resource ? "Resource saved" : `${name} added`);
                onSaved();
                onClose();
              } catch (e) {
                toast.error(e);
              } finally {
                setBusy(false);
              }
            }}
          >
            {resource ? "Save" : "Add resource"}
          </Button>
        </>
      }
    >
      <Field label="Name" hint="How it appears in access rules and temporary access.">
        <input value={name} autoFocus onChange={(e) => setName(e.target.value)} placeholder="e.g. Orders database" />
      </Field>
      <Field
        label="Address"
        hint={
          <>
            An IP address (<code>10.0.1.25</code>) or a range (<code>10.0.1.0/24</code>). For RDS and other managed databases, use the range of the database's subnets: their address can change on failover.
          </>
        }
      >
        <input value={address} onChange={(e) => setAddress(e.target.value)} placeholder="10.0.1.0/24" className="mono" />
      </Field>
      <Field label="Description (optional)">
        <input value={description} onChange={(e) => setDescription(e.target.value)} placeholder="e.g. RDS PostgreSQL, production, eu-west-1" />
      </Field>
    </Modal>
  );
}

function AddRouter({ onClose, onChanged }: { onClose: () => void; onChanged: () => void }) {
  const toast = useToast();
  const tags = useAsync(() => get<ServerTag[]>("/admin/api/tags"));
  const [name, setName] = useState("vpc-router");
  const [routes, setRoutes] = useState("");
  const [key, setKey] = useState<EnrollKey | null>(null);
  const [busy, setBusy] = useState(false);
  const [joined, setJoined] = useState<SubnetRouter | null>(null);

  const list = routes
    .split(/[\s,]+/)
    .map((r) => r.trim())
    .filter(Boolean);
  const valid = list.length > 0 && list.every((r) => cidrRE.test(r) && r.includes("/"));

  // Once the command has run, wait for the router to appear.
  useEffect(() => {
    if (!key || joined) return;
    const id = window.setInterval(async () => {
      try {
        const n = await get<NetworksInfo>("/admin/api/networks");
        const r = n.routers.find((x) => x.name === name || x.name.startsWith(`${name}-`));
        if (r) {
          setJoined(r);
          onChanged();
        }
      } catch {
        /* keep waiting */
      }
    }, 4000);
    return () => window.clearInterval(id);
  }, [key, joined, name, onChanged]);

  const makeKey = async () => {
    setBusy(true);
    try {
      if (!(tags.data ?? []).some((t) => t.name === "tag:router" && t.managed)) {
        await put(`/admin/api/tags/${encodeURIComponent("tag:router")}`, { description: "Subnet routers into private networks", access: [] });
      }
      setKey(await post<EnrollKey>(`/admin/api/tags/${encodeURIComponent("tag:router")}/enroll`, { reusable: false, ephemeral: false, expiresInHours: 24 }));
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const approve = async () => {
    if (!joined) return;
    setBusy(true);
    try {
      await post(`/api/v1/node/${joined.id}/approve_routes`, { routes: [...new Set([...joined.approved, ...joined.pending])] });
      toast.ok("Routes approved");
      onChanged();
      onClose();
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const command = key
    ? [
        "# Let Linux forward traffic",
        "echo 'net.ipv4.ip_forward = 1' | sudo tee /etc/sysctl.d/99-orgscale.conf",
        "echo 'net.ipv6.conf.all.forwarding = 1' | sudo tee -a /etc/sysctl.d/99-orgscale.conf",
        "sudo sysctl -p /etc/sysctl.d/99-orgscale.conf",
        "",
        "# Install Tailscale and join as a subnet router",
        "curl -fsSL https://tailscale.com/install.sh | sh",
        `sudo tailscale up --login-server=${key.serverUrl} --auth-key=${key.key} --hostname=${name} --advertise-routes=${list.join(",")}`,
      ].join("\n")
    : "";

  return (
    <Modal
      title="Add a subnet router"
      onClose={onClose}
      wide
      footer={
        joined ? (
          <Button variant="primary" busy={busy} onClick={approve}>
            Approve {joined.pending.length ? joined.pending.join(", ") : "routes"}
          </Button>
        ) : key ? (
          <Button variant="ghost" onClick={onClose}>
            Close
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button variant="primary" busy={busy} disabled={!valid || !/^[a-z0-9-]+$/.test(name)} onClick={makeKey}>
              Get install command
            </Button>
          </>
        )
      }
    >
      {!key ? (
        <>
          <p className="muted">
            A subnet router is one small Linux machine inside the private network (an EC2 instance in the VPC, an Azure VM in the VNet, a server in the office) that forwards traffic for the network's addresses. RDS and other managed services cannot run Tailscale themselves; they are reached through it.
          </p>
          <div className="grid-2">
            <Field label="Name" hint="Lowercase letters, digits and dashes.">
              <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} placeholder="aws-prod-vpc" />
            </Field>
            <Field label="Network ranges" hint="The VPC or subnet ranges it serves, comma separated.">
              <input value={routes} onChange={(e) => setRoutes(e.target.value)} placeholder="10.0.0.0/16" className="mono" />
            </Field>
          </div>
          <p className="muted small">It joins with the tag tag:router, so it belongs to no person. On AWS, also let its security group reach the databases on their ports; nothing else needs opening.</p>
        </>
      ) : (
        <>
          <p>Run this on the machine. The key works once, for 24 hours.</p>
          <CodeBlock code={command} />
          {joined ? (
            <div className="callout callout-ok">
              <b>{joined.name}</b> joined. Approve its routes so devices can use them.
            </div>
          ) : (
            <p className="muted small">
              <span className="spinner spinner-sm" aria-hidden /> Waiting for {name} to join… This window approves its routes once it does.
            </p>
          )}
        </>
      )}
    </Modal>
  );
}

function RouterRow({ r, canEdit, onChanged }: { r: SubnetRouter; canEdit: boolean; onChanged: () => void }) {
  const toast = useToast();
  const [busy, setBusy] = useState(false);
  return (
    <PRow
      label={
        <span>
          <Badge tone={r.online ? "green" : "neutral"}>{r.online ? "Online" : "Offline"}</Badge> <b>{r.name}</b> <span className="muted small">{r.tags.join(", ")}</span>
        </span>
      }
      sub={
        <>
          {r.approved.length > 0 && <>Routes: {r.approved.join(", ")}</>}
          {r.pending.length > 0 && <span className="text-warn"> {r.approved.length ? " · " : ""}Waiting for approval: {r.pending.join(", ")}</span>}
        </>
      }
    >
      {canEdit && r.pending.length > 0 && (
        <Button
          busy={busy}
          onClick={async () => {
            setBusy(true);
            try {
              await post(`/api/v1/node/${r.id}/approve_routes`, { routes: [...r.approved, ...r.pending] });
              toast.ok("Routes approved");
              onChanged();
            } catch (e) {
              toast.error(e);
            } finally {
              setBusy(false);
            }
          }}
        >
          Approve
        </Button>
      )}
    </PRow>
  );
}

export default function NetworksPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<NetworksInfo>("/admin/api/networks"));
  const [editing, setEditing] = useState<PrivateResource | "new" | null>(null);
  const [deleting, setDeleting] = useState<PrivateResource | null>(null);
  const [adding, setAdding] = useState(false);

  return (
    <>
      <PageHeader
        title="Networks"
        description="Reach private networks (AWS VPCs, Azure VNets, offices) and the managed services in them, such as RDS databases, through a subnet router. Name the resources here, then give access to them on Access or Temporary access."
        actions={
          canEdit && (
            <>
              <Button onClick={() => setAdding(true)}>Add subnet router</Button>
              <Button variant="primary" onClick={() => setEditing("new")}>
                <Icon name="plus" /> Add resource
              </Button>
            </>
          )
        }
      />
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : data ? (
        <>
          <Group title="Private resources" note="Each resource is reachable once a subnet router serves its address and an access rule or temporary access lets people in.">
            {data.resources.length === 0 ? (
              <PRow label={<span className="muted">No resources yet. Add one such as an RDS database, a subnet or a whole VPC.</span>} />
            ) : (
              data.resources.map((r) => (
                <PRow
                  key={r.id}
                  label={
                    <span>
                      <Icon name="globe" className="inline-icon" /> <b>{r.name}</b> <code>{r.address}</code>
                    </span>
                  }
                  sub={
                    <>
                      {r.description && <>{r.description} · </>}
                      {r.coveredBy.length ? <>Reached through {r.coveredBy.join(", ")}</> : <span className="text-warn">No subnet router serves this address yet</span>}
                    </>
                  }
                >
                  {canEdit && (
                    <div className="row">
                      <Button onClick={() => setEditing(r)}>Edit</Button>
                      <button className="icon-btn" aria-label={`Delete ${r.name}`} onClick={() => setDeleting(r)}>
                        <Icon name="trash" />
                      </button>
                    </div>
                  )}
                </PRow>
              ))
            )}
          </Group>

          <Group title="Subnet routers" note="Devices that forward traffic into a private network. Their routes work once approved.">
            {data.routers.length === 0 ? (
              <PRow label={<span className="muted">No subnet routers yet. Add one inside each private network.</span>} />
            ) : (
              data.routers.map((r) => <RouterRow key={r.id} r={r} canEdit={canEdit} onChanged={() => void reload()} />)
            )}
          </Group>
        </>
      ) : (
        <Empty title="Nothing here" />
      )}
      {editing && <ResourceEditor resource={editing === "new" ? undefined : editing} onClose={() => setEditing(null)} onSaved={() => void reload()} />}
      {adding && <AddRouter onClose={() => setAdding(false)} onChanged={() => void reload()} />}
      {deleting && (
        <Confirm
          title={`Delete ${deleting.name}?`}
          confirmLabel="Delete resource"
          danger
          onClose={() => setDeleting(null)}
          onConfirm={async () => {
            await del(`/admin/api/resources/${deleting.id}`);
            toast.ok("Resource deleted");
            void reload();
          }}
        >
          Access rules and temporary access naming it stop covering it. The network itself is not affected.
        </Confirm>
      )}
    </>
  );
}
