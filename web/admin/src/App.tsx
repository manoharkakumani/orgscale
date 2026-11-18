import { createContext, useContext, useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes } from "react-router-dom";
import { ApiError, get, post, setCSRFToken, type Me } from "./api";
import { Badge, Button, Icon, Spinner, ThemeSwitch } from "./components/ui";
import OverviewPage from "./pages/Overview";
import UsersPage from "./pages/Users";
import DevicesPage from "./pages/Devices";
import GroupsPage from "./pages/Groups";
import AccessPage from "./pages/Access";
import ServersPage from "./pages/Servers";
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

const nav: { group: string; items: { to: string; label: string; icon: string; end?: boolean }[] }[] = [
  {
    group: "Network",
    items: [
      { to: "/", label: "Overview", icon: "overview", end: true },
      { to: "/devices", label: "Devices", icon: "devices" },
      { to: "/servers", label: "Servers", icon: "server" },
      { to: "/access", label: "Access", icon: "shield" },
    ],
  },
  {
    group: "People",
    items: [
      { to: "/users", label: "Users", icon: "user" },
      { to: "/groups", label: "Groups", icon: "users" },
    ],
  },
  {
    group: "Administration",
    items: [
      { to: "/keys", label: "Keys", icon: "key" },
      { to: "/settings", label: "Settings", icon: "gear" },
      { to: "/audit", label: "Audit log", icon: "clock" },
    ],
  },
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
            {nav.map((g) => (
              <div key={g.group}>
                <div className="nav-group">{g.group}</div>
                {g.items.map((n) => (
                  <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
                    <Icon name={n.icon} />
                    {n.label}
                  </NavLink>
                ))}
              </div>
            ))}
          </nav>
          <div className="sidebar-foot muted" title={me.version}>
            orgscale {me.version.split("-")[0]}
          </div>
        </aside>
        <div className="main">
          <header className="topbar">
            <div className="muted topbar-server">{me.serverUrl}</div>
            <div className="topbar-user">
              <ThemeSwitch />
              {me.picture ? <img className="avatar" src={me.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-fallback">{(me.name || me.email).slice(0, 1).toUpperCase()}</span>}
              <div className="topbar-who">
                <div>{me.name || me.email}</div>
                {me.name && me.name !== me.email && <div className="muted small">{me.email}</div>}
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
              <Route path="/servers" element={<ServersPage />} />
              <Route path="/access" element={<AccessPage />} />
              <Route path="/policy" element={<Navigate to="/access" replace />} />
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
