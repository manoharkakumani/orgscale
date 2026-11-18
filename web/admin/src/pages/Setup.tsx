import { useEffect, useState } from "react";
import { ApiError, get, post, put, setCSRFToken, type SSOState } from "../api";
import { SSOFields, initialForm, issuerFor, type SSOForm } from "../components/sso";
import { Button, Field, Spinner, ThemeSwitch } from "../components/ui";

// First-time setup, opened from the one-time link the server prints. It
// configures single sign-on and names the first administrator; once that
// administrator signs in, setup closes for good.

type Phase = "starting" | "no-link" | "expired" | "form" | "done";

const emailRe = /^[^\s@]+@[^\s@]+\.[^\s@]+$/;

export default function SetupPage() {
  const [phase, setPhase] = useState<Phase>("starting");
  const [error, setError] = useState("");
  const [state, setState] = useState<SSOState | null>(null);
  const [form, setForm] = useState<SSOForm>(initialForm());
  const [adminEmail, setAdminEmail] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    const token = window.location.hash.slice(1);
    // Take the token out of the address bar and history at once.
    if (token) window.history.replaceState(null, "", window.location.pathname);

    (async () => {
      try {
        if (token) {
          const s = await post<{ csrfToken: string }>("/admin/setup/api/session", { token });
          setCSRFToken(s.csrfToken);
          sessionStorage.setItem("orgscale-setup-csrf", s.csrfToken);
        } else {
          const saved = sessionStorage.getItem("orgscale-setup-csrf");
          if (!saved) {
            setPhase("no-link");
            return;
          }
          setCSRFToken(saved);
        }
        const st = await get<SSOState>("/admin/setup/api/state");
        setState(st);
        setForm(initialForm(st));
        setPhase("form");
      } catch (e) {
        if (e instanceof ApiError && (e.status === 410 || e.status === 403 || e.status === 401)) {
          setError(e.message);
          setPhase("expired");
        } else {
          setError(e instanceof Error ? e.message : String(e));
          setPhase("expired");
        }
      }
    })();
  }, []);

  const finish = async () => {
    setBusy(true);
    setError("");
    try {
      const res = await put<{ loginUrl: string }>("/admin/setup/api/sso", {
        issuer: issuerFor(form),
        clientId: form.clientId,
        clientSecret: form.clientSecret,
        adminEmail,
      });
      sessionStorage.removeItem("orgscale-setup-csrf");
      setPhase("done");
      setTimeout(() => window.location.assign(res.loginUrl), 1200);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
    } finally {
      setBusy(false);
    }
  };

  const ready = !!issuerFor(form) && !!form.clientId.trim() && (!!form.clientSecret.trim() || !!state?.hasSecret) && emailRe.test(adminEmail.trim());

  return (
    <div className="setup">
      <div className="setup-top">
        <div className="brand">
          <span className="logo" aria-hidden />
          orgscale
        </div>
        <ThemeSwitch />
      </div>
      <div className="setup-card">
        {phase === "starting" && <Spinner />}

        {phase === "no-link" && (
          <>
            <h1>Set up orgscale</h1>
            <p className="muted">Open the setup link from the server's log. To make a new one, run this on the server:</p>
            <pre className="codeblock">orgscale admin setup-link</pre>
            <p className="muted small mt">With Docker: docker compose exec orgscale orgscale admin setup-link</p>
          </>
        )}

        {phase === "expired" && (
          <>
            <h1>This setup link can't be used</h1>
            <div className="callout callout-warn">{error || "The link expired or was already used."}</div>
            <p className="muted">Links work once and expire after 30 minutes. Make a new one on the server:</p>
            <pre className="codeblock">orgscale admin setup-link</pre>
          </>
        )}

        {phase === "form" && state && (
          <>
            <h1>Connect your identity provider</h1>
            <p className="muted">
              People and administrators sign in with your company accounts. There is no orgscale password: access follows your identity provider, including when people leave.
            </p>
            {state.fromConfig ? (
              <div className="callout callout-info">Single sign-on is set in the server's configuration file ({state.issuer}). Change it there.</div>
            ) : (
              <SSOFields state={state} form={form} onChange={setForm} />
            )}
            <Field label="First administrator" hint="Your work email at that identity provider. Only this person can sign in to finish setup; add more administrators later.">
              <input type="email" value={adminEmail} placeholder="you@company.com" onChange={(e) => setAdminEmail(e.target.value)} />
            </Field>
            {error && <div className="callout callout-error">{error}</div>}
            <div className="savebar">
              <Button variant="primary" className="btn-lg" busy={busy} disabled={!ready || state.fromConfig} onClick={finish}>
                Connect and sign in
              </Button>
            </div>
            <p className="muted small">orgscale checks the provider before saving. You'll then sign in with it; when you do, this setup link stops working.</p>
          </>
        )}

        {phase === "done" && (
          <>
            <h1>Connected</h1>
            <p className="muted">Taking you to your identity provider to sign in…</p>
            <Spinner />
          </>
        )}
      </div>
    </div>
  );
}
