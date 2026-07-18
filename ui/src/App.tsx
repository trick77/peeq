import { useEffect, useState } from "react";
import { Rail, type ViewId } from "./shell/Rail";
import { TopBar } from "./shell/TopBar";
import { getMe, listDownloads, cookieHealth } from "./api";
import type { Job, User } from "./api/types";

// Page titles/subtitles per view, per the mockup's `titles` map. Task 14
// will make these live (video counts, disk usage, etc); Task 13 only wires
// the shell that renders them.
const VIEW_META: Record<ViewId, { title: string; subtitle?: string }> = {
  library: { title: "Library" },
  player: { title: "Now playing" },
  add: { title: "Add a video" },
  pending: { title: "New & pending" },
  settings: { title: "Settings" },
};

// App — the shell (rail + topbar + routed main). View pages themselves
// (Library/Add/Player/Settings) are placeholders here; Task 14 replaces
// each with the real page. Routing is manual view-state, no router lib —
// matches loom's pattern for a single-page app this size.
export function App() {
  const [view, setView] = useState<ViewId>("library");
  const [user, setUser] = useState<User | null>(null);
  const [authChecked, setAuthChecked] = useState(false);
  const [jobs, setJobs] = useState<Job[]>([]);
  const [cookieStatus, setCookieStatus] = useState<string | undefined>(undefined);

  useEffect(() => {
    let active = true;
    getMe()
      .then((u) => {
        if (active) setUser(u);
      })
      .finally(() => {
        if (active) setAuthChecked(true);
      });
    return () => {
      active = false;
    };
  }, []);

  useEffect(() => {
    if (!authChecked || !user) return;
    let active = true;
    listDownloads()
      .then((j) => {
        if (active) setJobs(j);
      })
      .catch(() => {});
    cookieHealth()
      .then((h) => {
        if (active) setCookieStatus(h.status);
      })
      .catch(() => {});
    return () => {
      active = false;
    };
  }, [authChecked, user]);

  if (!authChecked) {
    return (
      <div style={{ display: "grid", placeItems: "center", minHeight: "100vh" }}>
        <b>vark</b>
      </div>
    );
  }

  if (!user) {
    return (
      <div style={{ display: "grid", placeItems: "center", minHeight: "100vh" }}>
        <b>vark</b>
        <a href="/api/auth/login">Sign in</a>
      </div>
    );
  }

  const meta = VIEW_META[view];
  const pendingCount = jobs.filter((j) => j.state === "pending" || j.state === "running").length;

  return (
    <div className="app-shell">
      <Rail
        active={view}
        onNavigate={setView}
        pendingCount={pendingCount}
        jobs={jobs}
        cookieStatus={cookieStatus}
      />
      <main className="main">
        <TopBar title={meta.title} subtitle={meta.subtitle} showSearch={view === "library"} />
        <section className="page">
          <ViewPlaceholder view={view} />
        </section>
      </main>
    </div>
  );
}

// ViewPlaceholder stands in for the real Library/Add/Player/Pending/
// Settings pages, which Task 14 builds. Keeping the shell's routing wired
// end-to-end now means Task 14 only has to swap this switch's bodies.
function ViewPlaceholder({ view }: { view: ViewId }) {
  switch (view) {
    case "library":
      return <p>Library view — coming in Task 14.</p>;
    case "player":
      return <p>Now playing view — coming in Task 14.</p>;
    case "add":
      return <p>Add a video view — coming in Task 14.</p>;
    case "pending":
      return <p>New &amp; pending view — coming in Task 14.</p>;
    case "settings":
      return <p>Settings view — coming in Task 14.</p>;
  }
}
