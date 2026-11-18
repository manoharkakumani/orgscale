import { useState } from "react";
import type { SSOState } from "../api";
import { CopyText, Field, Group, PRow } from "./ui";

// The identity providers people pick from, with where to create the app and
// what the issuer looks like.
export const providers = [
  {
    id: "google",
    name: "Google Workspace",
    issuer: () => "https://accounts.google.com",
    steps: [
      "Open console.cloud.google.com → APIs & Services → OAuth consent screen. Choose Internal and name the app orgscale.",
      "Credentials → Create credentials → OAuth client ID → Web application.",
      "Add both redirect URLs below as Authorized redirect URIs, then create.",
      "Copy the Client ID and Client secret here.",
    ],
  },
  {
    id: "entra",
    name: "Microsoft Entra ID",
    issuer: (tenant: string) => `https://login.microsoftonline.com/${tenant || "<tenant-id>"}/v2.0`,
    needsTenant: true,
    steps: [
      "Open entra.microsoft.com → App registrations → New registration. Name it orgscale.",
      "Redirect URI: platform Web, the first URL below. Add the second under Authentication afterwards.",
      "Copy the Directory (tenant) ID and Application (client) ID here.",
      "Certificates & secrets → New client secret; copy its Value here.",
    ],
  },
  {
    id: "okta",
    name: "Okta",
    issuer: (domain: string) => `https://${domain || "<your-org>.okta.com"}`,
    needsDomain: true,
    steps: [
      "Okta admin → Applications → Create App Integration → OIDC - OpenID Connect → Web Application.",
      "Sign-in redirect URIs: both URLs below. Assign the people or groups who may use the VPN.",
      "Copy the Client ID and Client secret here, and your Okta domain.",
    ],
  },
  {
    id: "other",
    name: "Other (Keycloak, Authentik, …)",
    issuer: (v: string) => v,
    needsIssuer: true,
    steps: ["Create an OpenID Connect web application with both redirect URLs below.", "Copy its issuer URL, client ID and client secret here."],
  },
] as const;

export type ProviderId = (typeof providers)[number]["id"];

export type SSOForm = { provider: ProviderId; extra: string; issuer: string; clientId: string; clientSecret: string };

export function guessProvider(issuer: string): SSOForm["provider"] {
  if (issuer.includes("accounts.google.com")) return "google";
  if (issuer.includes("login.microsoftonline.com")) return "entra";
  if (issuer.includes("okta.com")) return "okta";
  return issuer ? "other" : "google";
}

export function issuerFor(f: SSOForm): string {
  const p = providers.find((x) => x.id === f.provider)!;
  return p.id === "other" ? f.issuer.trim() : p.issuer(f.extra.trim());
}

export function initialForm(s?: SSOState): SSOForm {
  const provider = guessProvider(s?.issuer ?? "");
  let extra = "";
  if (s?.issuer && provider === "entra") extra = s.issuer.split("/")[3] ?? "";
  if (s?.issuer && provider === "okta") extra = s.issuer.replace(/^https:\/\//, "");
  return { provider, extra, issuer: provider === "other" ? (s?.issuer ?? "") : "", clientId: s?.clientId ?? "", clientSecret: "" };
}

// SSOFields edits single sign-on: provider, its instructions and redirect
// URLs, and the client credentials.
export function SSOFields({ state, form, onChange, disabled }: { state: SSOState; form: SSOForm; onChange: (f: SSOForm) => void; disabled?: boolean }) {
  const p = providers.find((x) => x.id === form.provider)!;
  const [showSteps, setShowSteps] = useState(!state.configured);

  return (
    <>
      <Field label="Identity provider">
        <div className="choice-grid">
          {providers.map((x) => (
            <button key={x.id} type="button" disabled={disabled} className={`choice ${form.provider === x.id ? "active" : ""}`} onClick={() => onChange({ ...form, provider: x.id })}>
              <b>{x.name}</b>
            </button>
          ))}
        </div>
      </Field>

      <Group title="Redirect URLs" note="Register both with your identity provider: the first for devices, the second for this console.">
        {state.redirectUrls.map((u, i) => (
          <PRow key={u} label={i === 0 ? "Devices" : "Admin console"} block>
            <CopyText value={u} />
          </PRow>
        ))}
      </Group>

      <details className="disclosure" open={showSteps} onToggle={(e) => setShowSteps((e.target as HTMLDetailsElement).open)}>
        <summary>How to create the app in {p.name}</summary>
        <ol className="small">
          {p.steps.map((s) => (
            <li key={s}>{s}</li>
          ))}
        </ol>
      </details>

      <div className="mt">
        {"needsTenant" in p && p.needsTenant && (
          <Field label="Directory (tenant) ID">
            <input value={form.extra} disabled={disabled} placeholder="00000000-0000-0000-0000-000000000000" onChange={(e) => onChange({ ...form, extra: e.target.value })} />
          </Field>
        )}
        {"needsDomain" in p && p.needsDomain && (
          <Field label="Okta domain">
            <input value={form.extra} disabled={disabled} placeholder="your-org.okta.com" onChange={(e) => onChange({ ...form, extra: e.target.value })} />
          </Field>
        )}
        {"needsIssuer" in p && p.needsIssuer && (
          <Field label="Issuer URL" hint="Your provider's OpenID Connect issuer, e.g. https://sso.example.com/realms/main">
            <input value={form.issuer} disabled={disabled} placeholder="https://" onChange={(e) => onChange({ ...form, issuer: e.target.value })} />
          </Field>
        )}
        <div className="grid-2">
          <Field label="Client ID">
            <input value={form.clientId} disabled={disabled} autoComplete="off" onChange={(e) => onChange({ ...form, clientId: e.target.value })} />
          </Field>
          <Field label="Client secret" hint={state.hasSecret ? "Stored. Leave empty to keep it." : undefined}>
            <input type="password" value={form.clientSecret} disabled={disabled} autoComplete="new-password" placeholder={state.hasSecret ? "••••••••" : ""} onChange={(e) => onChange({ ...form, clientSecret: e.target.value })} />
          </Field>
        </div>
        <div className="field-hint">Issuer: <code>{issuerFor(form) || "—"}</code></div>
      </div>
    </>
  );
}
