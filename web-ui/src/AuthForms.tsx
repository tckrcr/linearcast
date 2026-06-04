import { FormEvent, ReactNode, useState } from "react";
import { Link } from "react-router-dom";
import { loginAdmin, changeAdminPassword } from "./api/client";

export function AuthLoginForm({
  title,
  authEnabled,
  onAuthenticated,
  disabledNotice,
}: {
  title: string;
  authEnabled: boolean;
  onAuthenticated: (mustChange: boolean) => void;
  disabledNotice?: ReactNode;
}) {
  const [password, setPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      const status = await loginAdmin(password);
      if (status.authenticated) onAuthenticated(!!status.mustChange);
      else setError("Authentication required.");
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="admin-auth-page">
      <form className="admin-auth-panel" onSubmit={submit}>
        <h1>{title}</h1>
        {!authEnabled && disabledNotice}
        {authEnabled && (
          <>
            <label>
              <span>Password</span>
              <input
                type="password"
                value={password}
                autoFocus
                autoComplete="current-password"
                onChange={(e) => setPassword(e.target.value)}
              />
            </label>
            {error && <p className="form-status error">{error}</p>}
            <button type="submit" disabled={busy || password.trim() === ""}>
              {busy ? "Signing in..." : "Sign in"}
            </button>
          </>
        )}
        <Link to="/" className="admin-auth-live-link">Back to live</Link>
      </form>
    </div>
  );
}

export function ChangePasswordForm({ onChanged }: { onChanged: () => void }) {
  const [currentPassword, setCurrentPassword] = useState("");
  const [newPassword, setNewPassword] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    if (newPassword !== confirmPassword) {
      setError("New passwords do not match.");
      return;
    }
    setBusy(true);
    setError("");
    try {
      await changeAdminPassword(currentPassword, newPassword);
      onChanged();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const canSubmit =
    !busy && currentPassword !== "" && newPassword.trim().length >= 8 && confirmPassword !== "";

  return (
    <div className="admin-auth-page">
      <form className="admin-auth-panel" onSubmit={submit}>
        <h1>Change password</h1>
        <p className="muted">A default password is set. Choose a new password before continuing.</p>
        <label>
          <span>Current password</span>
          <input
            type="password"
            value={currentPassword}
            autoFocus
            autoComplete="current-password"
            onChange={(e) => setCurrentPassword(e.target.value)}
          />
        </label>
        <label>
          <span>New password</span>
          <input
            type="password"
            value={newPassword}
            autoComplete="new-password"
            onChange={(e) => setNewPassword(e.target.value)}
          />
        </label>
        <label>
          <span>Confirm new password</span>
          <input
            type="password"
            value={confirmPassword}
            autoComplete="new-password"
            onChange={(e) => setConfirmPassword(e.target.value)}
          />
        </label>
        {error && <p className="form-status error">{error}</p>}
        <button type="submit" disabled={!canSubmit}>
          {busy ? "Saving..." : "Set password"}
        </button>
      </form>
    </div>
  );
}
