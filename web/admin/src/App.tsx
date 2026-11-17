import { createContext, useContext, useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { ApiError, get, post, setCSRFToken, type Me } from "./api";
import { Badge, Button, Spinner } from "./components/ui";
import OverviewPage from "./pages/Overview";
import UsersPage from "./pages/Users";
import DevicesPage from "./pages/Devices";
import GroupsPage from "./pages/Groups";
import PolicyPage from "./pages/Policy";
import KeysPage from "./pages/Keys";
import SettingsPage from "./pages/Settings";
import AuditPage from "./pages/Audit";

const MeCtx = createContext<Me | null>(null);

export function useMe(): Me {
  const me = useContext(MeCtx);
  if (!me) throw new Error("useMe outside the signed-in app");
  return me;
}

export function useCanEdit(): boolean {
  return useMe().role === "admin";
}

const nav = [
  { to: "/", label: "Overview", end: true },
  { to: "/users", label: "Users" },
  { to: "/devices", label: "Devices" },
  { to: "/groups", label: "Groups" },
  { to: "/policy", label: "Access policy" },
  { to: "/keys", label: "Keys" },
  { to: "/settings", label: "Settings" },
  { to: "/audit", label: "Audit log" },
];

function SignIn({ error }: { error?: string }) {
  return (
    <div className="signin">
      <div className="signin-card">
        <div className="brand brand-lg">
          <span className="logo" aria-hidden />
          orgscale
        </div>
        <p className="muted">Sign in with your organization's single sign-on to manage users, devices and access.</p>
        {error && <div className="callout callout-error">{error}</div>}
        <a className="btn btn-primary btn-block" href="/admin/auth/login">
          Sign in with SSO
        </a>
      </div>
    </div>
  );
}

export default function App() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const [error, setError] = useState("");

  const load = async () => {
    try {
      const m = await get<Me>("/admin/api/me");
      setCSRFToken(m.csrfToken);
      setMe(m);
    } catch (e) {
      if (!(e instanceof ApiError && e.status === 401)) setError(e instanceof Error ? e.message : String(e));
      setMe(null);
    }
  };

  useEffect(() => {
    void load();
    const onSignedOut = () => setMe(null);
    window.addEventListener("orgscale:signed-out", onSignedOut);
    return () => window.removeEventListener("orgscale:signed-out", onSignedOut);
  }, []);

  if (me === undefined) return <Spinner />;
  if (me === null) return <SignIn error={error} />;

  const signOut = async () => {
    try {
      await post("/admin/auth/logout");
    } finally {
      setMe(null);
    }
  };

  return (
    <MeCtx.Provider value={me}>
      <div className="shell">
        <aside className="sidebar">
          <div className="brand">
            <span className="logo" aria-hidden />
            orgscale
          </div>
          <nav>
            {nav.map((n) => (
              <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
                {n.label}
              </NavLink>
            ))}
          </nav>
          <div className="sidebar-foot muted">{me.version}</div>
        </aside>
        <div className="main">
          <header className="topbar">
            <div className="muted topbar-server">{me.serverUrl}</div>
            <div className="topbar-user">
              {me.picture ? <img className="avatar" src={me.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-fallback">{(me.name || me.email).slice(0, 1).toUpperCase()}</span>}
              <div className="topbar-who">
                <div>{me.name || me.email}</div>
                <div className="muted small">{me.email}</div>
              </div>
              <Badge tone={me.role === "admin" ? "blue" : "neutral"}>{me.role === "admin" ? "Admin" : "Read-only"}</Badge>
              <Button variant="ghost" onClick={signOut}>
                Sign out
              </Button>
            </div>
          </header>
          <main className="content">
            <Routes>
              <Route path="/" element={<OverviewPage />} />
              <Route path="/users" element={<UsersPage />} />
              <Route path="/devices" element={<DevicesPage />} />
              <Route path="/groups" element={<GroupsPage />} />
              <Route path="/policy" element={<PolicyPage />} />
              <Route path="/keys" element={<KeysPage />} />
              <Route path="/settings" element={<SettingsPage />} />
              <Route path="/audit" element={<AuditPage />} />
              <Route path="*" element={<Navigate to="/" replace />} />
            </Routes>
          </main>
        </div>
      </div>
    </MeCtx.Provider>
  );
}
