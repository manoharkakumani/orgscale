import { useEffect, useState } from "react";
import { get, post, put, type PolicyInfo } from "../api";
import { useCanEdit } from "../App";
import { Button, ErrorBox, PageHeader, Spinner, useAsync, useToast } from "../components/ui";

const starter = `{
  // Groups from your identity provider are available as group:<name>
  // (see the Groups page) without listing them here.
  "acls": [
    // Example: let the engineering group reach staging servers on SSH and HTTPS.
    // {"action": "accept", "src": ["group:engineering"], "dst": ["tag:staging:22,443"]},
  ],
  "tagOwners": {
    // "tag:server": ["group:it-admins"],
  },
}
`;

export default function PolicyPage() {
  const canEdit = useCanEdit();
  const toast = useToast();
  const { data, error, loading, reload } = useAsync(() => get<PolicyInfo>("/admin/api/policy"));
  const [text, setText] = useState("");
  const [check, setCheck] = useState<{ ok: boolean; msg: string } | null>(null);
  const [busy, setBusy] = useState<"check" | "save" | "">("");
  const [showEffective, setShowEffective] = useState(false);

  useEffect(() => {
    if (data) setText(data.policy || starter);
  }, [data]);

  if (loading && !data) return <Spinner />;
  if (error) return <ErrorBox error={error} onRetry={reload} />;
  if (!data) return null;

  const editable = canEdit && data.editable;
  const dirty = text !== (data.policy || starter);

  const validate = async () => {
    setBusy("check");
    try {
      await post("/api/v1/policy/check", { policy: text });
      setCheck({ ok: true, msg: "The policy is valid." });
    } catch (e) {
      setCheck({ ok: false, msg: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy("");
    }
  };

  const save = async () => {
    setBusy("save");
    try {
      await put("/api/v1/policy", { policy: text });
      setCheck({ ok: true, msg: "Saved. Clients receive the new rules within seconds." });
      toast.ok("Policy saved");
      void reload();
    } catch (e) {
      setCheck({ ok: false, msg: e instanceof Error ? e.message : String(e) });
    } finally {
      setBusy("");
    }
  };

  return (
    <>
      <PageHeader
        title="Access policy"
        description="Rules for who can reach what, in Tailscale's policy format (HuJSON: JSON with comments and trailing commas). Device assignments and IdP groups are added automatically; you don't need to write them here."
        actions={
          editable && (
            <>
              <Button onClick={validate} busy={busy === "check"}>
                Validate
              </Button>
              <Button variant="primary" onClick={save} busy={busy === "save"} disabled={!dirty}>
                Save
              </Button>
            </>
          )
        }
      />

      {!data.editable && (
        <div className="callout callout-warn">
          The policy is read from a file (<code>policy.mode: {data.mode}</code>). To edit it here, set <code>policy.mode: database</code> in the configuration file.
        </div>
      )}
      {data.allowsEverything && (
        <div className="callout callout-warn">
          This policy has a rule letting everyone reach everything (<code>"src": ["*"]</code> to <code>"*:*"</code>). Device restriction can't narrow that; remove the rule to use device assignments.
        </div>
      )}
      {check && <div className={`callout ${check.ok ? "callout-ok" : "callout-error"}`}>{check.msg}</div>}

      <textarea
        className="code-editor card"
        spellCheck={false}
        value={text}
        readOnly={!editable}
        onChange={(e) => {
          setText(e.target.value);
          setCheck(null);
        }}
        onKeyDown={(e) => {
          if ((e.metaKey || e.ctrlKey) && e.key === "s" && editable) {
            e.preventDefault();
            void save();
          }
        }}
        aria-label="Policy"
      />

      <div className="mt">
        <Button variant="ghost" onClick={() => setShowEffective((v) => !v)}>
          {showEffective ? "Hide" : "Show"} the effective policy (with generated rules)
        </Button>
        {showEffective && <pre className="code-view card">{data.effective || "(no policy: everyone can reach everything)"}</pre>}
      </div>
    </>
  );
}
