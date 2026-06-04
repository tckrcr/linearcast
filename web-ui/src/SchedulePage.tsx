import { lazy, Suspense, useEffect, useState } from "react";
import { useNavigate } from "react-router-dom";
import { getAdminAuthStatus } from "./api/client";
import { AuthLoginForm, ChangePasswordForm } from "./AuthForms";

const ScheduleBuilderPanel = lazy(() =>
  import("./panels/ScheduleBuilderPanel").then((m) => ({ default: m.ScheduleBuilderPanel }))
);

export function SchedulePage() {
  const navigate = useNavigate();
  const [authState, setAuthState] = useState<"checking" | "authenticated" | "must-change" | "unauthenticated">("checking");
  const [authEnabled, setAuthEnabled] = useState(false);

  useEffect(() => {
    let cancelled = false;
    getAdminAuthStatus()
      .then((status) => {
        if (cancelled) return;
        setAuthEnabled(status.enabled);
        if (status.authenticated) {
          setAuthState(status.mustChange ? "must-change" : "authenticated");
        } else {
          setAuthState("unauthenticated");
        }
      })
      .catch(() => {
        if (!cancelled) setAuthState("unauthenticated");
      });
    return () => { cancelled = true; };
  }, []);

  if (authState === "checking") {
    return (
      <div className="admin-auth-page">
        <div className="admin-auth-panel">
          <h1>Schedule</h1>
          <p className="muted">checking session...</p>
        </div>
      </div>
    );
  }

  if (authState === "unauthenticated") {
    return (
      <AuthLoginForm
        title="Schedule"
        authEnabled={authEnabled}
        onAuthenticated={(mustChange) => setAuthState(mustChange ? "must-change" : "authenticated")}
      />
    );
  }

  if (authState === "must-change") {
    return (
      <ChangePasswordForm onChanged={() => setAuthState("authenticated")} />
    );
  }

  return (
    <div className="admin-page">
      <header className="admin-page-header">
        <div className="admin-page-brand">
          <a className="admin-back-link" href="/">← Watch</a>
          <span className="admin-page-title">Schedule</span>
        </div>
        <a className="admin-back-link" href="/admin">Admin</a>
      </header>
      <div className="admin-page-body">
        <main className="admin-main">
          <Suspense fallback={<div className="admin-panel"><section className="admin-panel-section"><p className="muted">loading...</p></section></div>}>
            <ScheduleBuilderPanel
              onChannelImported={() => navigate("/")}
            />
          </Suspense>
        </main>
      </div>
    </div>
  );
}

