import { useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { get, post, put, type EnrollKey, type RecordingInfo, type ServerTag } from "../api";
import { useCanEdit } from "../App";
import {
  Badge,
  Button,
  CodeBlock,
  Empty,
  ErrorBox,
  Field,
  Group,
  Modal,
  PRow,
  PageHeader,
  Segmented,
  Spinner,
  Toggle,
  useAsync,
  useToast,
} from "../components/ui";

function AddRecorder({ onClose, onChanged }: { onClose: () => void; onChanged: () => void }) {
  const toast = useToast();
  const [name, setName] = useState("recorder");
  const [storage, setStorage] = useState<"disk" | "s3">("disk");
  const [bucket, setBucket] = useState("");
  const [key, setKey] = useState<EnrollKey | null>(null);
  const [busy, setBusy] = useState(false);
  const [joined, setJoined] = useState(false);

  useEffect(() => {
    if (!key || joined) return;
    const id = window.setInterval(async () => {
      try {
        const r = await get<RecordingInfo>("/admin/api/recording");
        if (r.recorders.some((x) => x.name === name || x.name.startsWith(`${name}-`))) {
          setJoined(true);
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
      const tags = await get<ServerTag[]>("/admin/api/tags");
      if (!tags.some((t) => t.name === "tag:recorder" && t.managed)) {
        await put(`/admin/api/tags/${encodeURIComponent("tag:recorder")}`, {
          description: "SSH session recorders",
          access: [],
        });
      }
      setKey(
        await post<EnrollKey>(`/admin/api/tags/${encodeURIComponent("tag:recorder")}/enroll`, {
          reusable: false,
          ephemeral: false,
          expiresInHours: 24,
        }),
      );
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  const dst = storage === "s3" ? `--bucket=${bucket.trim()}` : "--dst=/data/recordings";
  const command = key
    ? [
        "docker run -d --name tsrecorder --restart unless-stopped \\",
        "  -v /var/lib/tsrecorder:/data \\",
        `  -e TS_CONTROL_URL=${key.serverUrl} \\`,
        `  -e TS_AUTHKEY=${key.key} \\`,
        ...(storage === "s3"
          ? ["  -e AWS_REGION=<region> \\", "  # plus AWS credentials, or run it on EC2 with an instance role \\"]
          : []),
        "  tailscale/tsrecorder:stable \\",
        `  /tsrecorder --hostname=${name} --statedir=/data/state ${dst}`,
      ].join("\n")
    : "";

  return (
    <Modal
      title="Add a session recorder"
      onClose={onClose}
      wide
      footer={
        key ? (
          <Button variant={joined ? "primary" : "ghost"} onClick={onClose}>
            {joined ? "Done" : "Close"}
          </Button>
        ) : (
          <>
            <Button variant="ghost" onClick={onClose}>
              Cancel
            </Button>
            <Button
              variant="primary"
              busy={busy}
              disabled={!/^[a-z0-9-]+$/.test(name) || (storage === "s3" && !/^s3:\/\/[a-z0-9.-]+/.test(bucket.trim()))}
              onClick={makeKey}
            >
              Get install command
            </Button>
          </>
        )
      }
    >
      {!key ? (
        <>
          <p className="muted">
            A recorder runs Tailscale's <b>tsrecorder</b> on any machine with Docker. SSH servers send it every session
            as it happens; it writes them to its disk or to S3. Recordings never pass through orgscale.
          </p>
          <div className="grid-2">
            <Field label="Name" hint="Lowercase letters, digits and dashes.">
              <input value={name} onChange={(e) => setName(e.target.value.toLowerCase())} />
            </Field>
            <Field label="Store recordings">
              <Segmented<"disk" | "s3">
                value={storage}
                options={[
                  { value: "disk", label: "On its disk" },
                  { value: "s3", label: "In S3" },
                ]}
                onChange={setStorage}
              />
            </Field>
          </div>
          {storage === "s3" && (
            <Field label="Bucket" hint="It needs permission to write objects to it.">
              <input
                className="mono"
                value={bucket}
                onChange={(e) => setBucket(e.target.value)}
                placeholder="s3://ssh-recordings"
              />
            </Field>
          )}
        </>
      ) : (
        <>
          <p>Run this on the machine. The key works once, for 24 hours.</p>
          <CodeBlock code={command} />
          {joined ? (
            <div className="callout callout-ok">
              <b>{name}</b> joined. SSH sessions are recorded from now on.
            </div>
          ) : (
            <p className="muted small">
              <span className="spinner spinner-sm" aria-hidden /> Waiting for {name} to join…
            </p>
          )}
          {storage === "disk" && (
            <p className="muted small">
              Recordings land in /var/lib/tsrecorder/recordings, one .cast file per session; play them with asciinema.
            </p>
          )}
        </>
      )}
    </Modal>
  );
}

export default function RecordingPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload, setData } = useAsync(() => get<RecordingInfo>("/admin/api/recording"));
  const [busy, setBusy] = useState(false);
  const [adding, setAdding] = useState(false);

  const save = async (next: { enabled: boolean; enforce: boolean }) => {
    setBusy(true);
    try {
      setData(await put<RecordingInfo>("/admin/api/recording", next));
      toast.ok(next.enabled ? "Session recording on" : "Session recording off");
    } catch (e) {
      toast.error(e);
    } finally {
      setBusy(false);
    }
  };

  return (
    <>
      <PageHeader
        title="Session recording"
        description={
          <>
            Record every Tailscale SSH session to servers: what was typed and shown, for audits and incident review.
            Covers SSH given on <Link to="/servers">Servers</Link>, <Link to="/access">Access</Link> and{" "}
            <Link to="/temporary-access">Temporary access</Link>.
          </>
        }
        actions={
          canEdit && (
            <Button variant="primary" onClick={() => setAdding(true)}>
              Add recorder
            </Button>
          )
        }
      />
      {error && <ErrorBox error={error} onRetry={reload} />}
      {loading && !data ? (
        <Spinner />
      ) : data ? (
        <>
          {data.enabled && data.recorders.length === 0 && (
            <div className="callout callout-warn">
              {data.enforce
                ? "No recorder has joined yet, so every SSH session is refused."
                : "No recorder has joined yet, so sessions are not recorded."}{" "}
              Add one to start recording.
            </div>
          )}
          <Group title="Recording">
            <PRow
              label="Record SSH sessions"
              sub="Applies to new sessions. Sessions over plain SSH with keys or passwords, outside Tailscale SSH, are not seen."
            >
              <Toggle
                checked={data.enabled}
                disabled={!canEdit || busy}
                label="Record SSH sessions"
                hideLabel
                onChange={(enabled) => void save({ enabled, enforce: enabled && data.enforce })}
              />
            </PRow>
            {data.enabled && (
              <PRow
                label="If no recorder answers"
                sub={
                  data.enforce
                    ? "Sessions are refused, and a session ends if its recorder stops answering. Nothing goes unrecorded."
                    : "Sessions go ahead unrecorded, so a recorder outage never locks anyone out."
                }
              >
                <Segmented<"open" | "closed">
                  value={data.enforce ? "closed" : "open"}
                  disabled={!canEdit || busy}
                  options={[
                    { value: "open", label: "Allow" },
                    { value: "closed", label: "Refuse" },
                  ]}
                  onChange={(v) => void save({ enabled: true, enforce: v === "closed" })}
                />
              </PRow>
            )}
          </Group>

          <Group title="Recorders" note={`Devices tagged ${data.tag}. With more than one, servers try each in turn.`}>
            {data.recorders.length === 0 ? (
              <PRow label={<span className="muted">No recorders yet.</span>} />
            ) : (
              data.recorders.map((r) => (
                <PRow
                  key={r.id}
                  label={
                    <span>
                      <Badge tone={r.online ? "green" : "neutral"}>{r.online ? "Online" : "Offline"}</Badge>{" "}
                      <b>{r.name}</b>
                    </span>
                  }
                  sub={r.address && <code>{r.address}:80</code>}
                />
              ))
            )}
          </Group>
        </>
      ) : (
        <Empty title="Nothing here" />
      )}
      {adding && <AddRecorder onClose={() => setAdding(false)} onChanged={() => void reload()} />}
    </>
  );
}
