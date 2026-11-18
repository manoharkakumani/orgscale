import { createContext, useContext, useEffect, useState } from "react";
import { NavLink, Navigate, Route, Routes, useLocation } from "react-router-dom";
import { ApiError, post, setCSRFToken, type Me } from "./api";
import { Badge, Button, Icon, Spinner, ThemeSwitch } from "./components/ui";
import OverviewPage from "./pages/Overview";
import UsersPage from "./pages/Users";
import DevicesPage from "./pages/Devices";
import GroupsPage from "./pages/Groups";
import AccessPage from "./pages/Access";
import ServersPage from "./pages/Servers";
import KeysPage from "./pages/Keys";
import SettingsPage from "./pages/Settings";
import SetupPage from "./pages/Setup";
import AuditPage from "./pages/Audit";
import RequestsPage, { MyAccessPage } from "./pages/Requests";
import { get, type AccessRequest } from "./api";

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
      { to: "/requests", label: "Requests", icon: "clock" },
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

// People who are not administrators only request access.
const memberNav: typeof nav = [{ group: "You", items: [{ to: "/", label: "Request access", icon: "key", end: true }] }];

function pageTitle(path: string, items: typeof nav): string {
  for (const g of items) for (const n of g.items) if (n.to !== "/" && path.startsWith(n.to)) return n.label;
  return items[0].items[0].label;
}

const roleLabel = { admin: "Admin", viewer: "Read-only", member: "Member" } as const;

/** usePendingRequests counts requests waiting for a decision, for the nav. */
function usePendingRequests(enabled: boolean): number {
  const [n, setN] = useState(0);
  useEffect(() => {
    if (!enabled) return;
    const load = () =>
      get<AccessRequest[]>("/admin/api/requests")
        .then((rs) => setN(rs.filter((r) => r.status === "pending").length))
        .catch(() => undefined);
    void load();
    const id = window.setInterval(load, 30000);
    return () => window.clearInterval(id);
  }, [enabled]);
  return n;
}

function SignIn({ error, setupRequired }: { error?: string; setupRequired?: boolean }) {
  return (
    <div className="signin">
      <div className="signin-card">
        <div className="brand brand-lg">
          <span className="logo" aria-hidden />
          orgscale
        </div>
        {setupRequired ? (
          <>
            <p className="muted">orgscale isn't set up yet. Open the setup link from the server's log, or make a new one on the server:</p>
            <pre className="codeblock">orgscale admin setup-link</pre>
          </>
        ) : (
          <>
            <p className="muted">Sign in with your organization's single sign-on to manage users, devices and access.</p>
            {error && <div className="callout callout-error">{error}</div>}
            <a className="btn btn-primary btn-block" href="/admin/auth/login">
              Sign in with SSO
            </a>
          </>
        )}
      </div>
    </div>
  );
}

export default function App() {
  const location = useLocation();
  if (location.pathname === "/setup") return <SetupPage />;
  return <Console />;
}

function Console() {
  const [me, setMe] = useState<Me | null | undefined>(undefined);
  const [error, setError] = useState("");
  const [setupRequired, setSetupRequired] = useState(false);
  const [menuOpen, setMenuOpen] = useState(false);
  const location = useLocation();

  const [scrolled, setScrolled] = useState(false);

  // Close the phone menu after navigating.
  useEffect(() => setMenuOpen(false), [location.pathname]);

  // The top bar shows the page name once the large title scrolls away.
  useEffect(() => {
    const onScroll = () => setScrolled(window.scrollY > 56);
    onScroll();
    window.addEventListener("scroll", onScroll, { passive: true });
    return () => window.removeEventListener("scroll", onScroll);
  }, [location.pathname]);

  // On phones table rows stack; each cell is labelled with its column.
  useEffect(() => {
    const label = () => {
      document.querySelectorAll(".table-wrap table").forEach((table) => {
        const heads = [...table.querySelectorAll("thead th")].map((th) => th.textContent?.trim() ?? "");
        table.querySelectorAll("tbody tr").forEach((tr) => {
          [...tr.children].forEach((td, i) => {
            if (heads[i] && !td.hasAttribute("data-label")) td.setAttribute("data-label", heads[i]);
          });
        });
      });
    };
    label();
    const main = document.querySelector(".content");
    if (!main) return;
    const obs = new MutationObserver(label);
    obs.observe(main, { childList: true, subtree: true });
    return () => obs.disconnect();
  });

  const load = async () => {
    try {
      const res = await fetch("/admin/api/me", { credentials: "same-origin" });
      const body = await res.json().catch(() => ({}));
      if (!res.ok) {
        setSetupRequired(!!body.setupRequired);
        if (res.status !== 401) throw new ApiError(res.status, body.error || res.statusText);
        setMe(null);
        return;
      }
      setCSRFToken(body.csrfToken);
      setMe(body as Me);
    } catch (e) {
      setError(e instanceof Error ? e.message : String(e));
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
  if (me === null) return <SignIn error={error} setupRequired={setupRequired} />;
  return <Shell me={me} setMe={setMe} menuOpen={menuOpen} setMenuOpen={setMenuOpen} scrolled={scrolled} />;
}

function Shell({ me, setMe, menuOpen, setMenuOpen, scrolled }: { me: Me; setMe: (m: Me | null) => void; menuOpen: boolean; setMenuOpen: (v: boolean) => void; scrolled: boolean }) {
  const location = useLocation();
  const member = me.role === "member";
  const items = member ? memberNav : nav;
  const pending = usePendingRequests(!member);

  const signOut = async () => {
    try {
      await post("/admin/auth/logout");
    } finally {
      setMe(null);
    }
  };

  return (
    <MeCtx.Provider value={me}>
      <div className={`shell ${menuOpen ? "menu-open" : ""} ${scrolled ? "scrolled" : ""}`}>
        <div className="menu-scrim" onClick={() => setMenuOpen(false)} />
        <aside className="sidebar" aria-label="Navigation">
          <div className="brand">
            <span className="logo" aria-hidden />
            orgscale
          </div>
          <nav>
            {items.map((g) => (
              <div key={g.group}>
                <div className="nav-group">{g.group}</div>
                {g.items.map((n) => (
                  <NavLink key={n.to} to={n.to} end={n.end} className={({ isActive }) => `nav-link ${isActive ? "active" : ""}`}>
                    <Icon name={n.icon} />
                    {n.label}
                    {n.to === "/requests" && pending > 0 && <span className="nav-count">{pending}</span>}
                  </NavLink>
                ))}
              </div>
            ))}
          </nav>
          <div className="sidebar-account">
            <div className="cell-user">
              {me.picture ? <img className="avatar" src={me.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-fallback">{(me.name || me.email).slice(0, 1).toUpperCase()}</span>}
              <div className="cell-stack grow">
                <span className="ellipsis">{me.name || me.email}</span>
                <span className="muted small ellipsis">{roleLabel[me.role]}</span>
              </div>
            </div>
            <div className="row-between">
              <ThemeSwitch />
              <Button variant="ghost" onClick={signOut}>
                Sign out
              </Button>
            </div>
          </div>
          <div className="sidebar-foot muted" title={me.version}>
            orgscale {me.version.split("-")[0]}
          </div>
        </aside>
        <div className="main">
          <header className="topbar">
            <button className="menu-btn" aria-label="Open menu" onClick={() => setMenuOpen(true)}>
              <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" strokeWidth={2} strokeLinecap="round" aria-hidden>
                <path d="M4 7h16M4 12h16M4 17h16" />
              </svg>
            </button>
            <div className="muted topbar-server">{me.serverUrl}</div>
            <div className="topbar-title">{pageTitle(location.pathname, items)}</div>
            <button className="topbar-avatar" aria-label="Account" onClick={() => setMenuOpen(true)}>
              {me.picture ? <img className="avatar" src={me.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-fallback">{(me.name || me.email).slice(0, 1).toUpperCase()}</span>}
            </button>
            <div className="topbar-user">
              <ThemeSwitch />
              {me.picture ? <img className="avatar" src={me.picture} alt="" referrerPolicy="no-referrer" /> : <span className="avatar avatar-fallback">{(me.name || me.email).slice(0, 1).toUpperCase()}</span>}
              <div className="topbar-who">
                <div>{me.name || me.email}</div>
                {me.name && me.name !== me.email && <div className="muted small">{me.email}</div>}
              </div>
              <Badge tone={me.role === "admin" ? "blue" : "neutral"}>{roleLabel[me.role]}</Badge>
              <Button variant="ghost" onClick={signOut}>
                Sign out
              </Button>
            </div>
          </header>
          <main className="content">
            {member ? (
              <Routes>
                <Route path="/" element={<MyAccessPage />} />
                <Route path="*" element={<Navigate to="/" replace />} />
              </Routes>
            ) : (
            <Routes>
              <Route path="/" element={<OverviewPage />} />
              <Route path="/requests" element={<RequestsPage />} />
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
            )}
          </main>
        </div>
      </div>
    </MeCtx.Provider>
  );
}
