import { lazy, Suspense, useEffect, useState } from "react";
import { Link } from "react-router-dom";
import { ChannelArtwork } from "./ChannelArtwork";
import { ChannelGuide } from "./ChannelGuide";
import { Dialog } from "./Dialog";
import {
  getMediaPackageProfileList,
  getAdminAuthStatus,
  logoutAdmin,
  UNAUTHORIZED_EVENT,
  useAdminNow,
  useChannelList,
} from "./api";
import { AuthLoginForm, ChangePasswordForm } from "./AuthForms";
import { formatMs } from "./format";
import {
  useChannelActions,
} from "./hooks/useChannelActions";
// Admin panels are lazy-loaded so each chunk only fetches on first selection.
const EncodingPanel = lazy(() =>
  import("./panels/EncodingPanel").then((m) => ({ default: m.EncodingPanel }))
);
const InventoryPanel = lazy(() =>
  import("./panels/InventoryPanel").then((m) => ({ default: m.InventoryPanel }))
);
const MediaSourcesPanel = lazy(() =>
  import("./panels/MediaSourcesPanel").then((m) => ({ default: m.MediaSourcesPanel }))
);
const ProfilesPanel = lazy(() =>
  import("./panels/ProfilesPanel").then((m) => ({ default: m.ProfilesPanel }))
);
const ToolsPanel = lazy(() =>
  import("./panels/ToolsPanel").then((m) => ({ default: m.ToolsPanel }))
);
const ScheduleBuilderPanel = lazy(() =>
  import("./panels/ScheduleBuilderPanel").then((m) => ({ default: m.ScheduleBuilderPanel }))
);
const OverviewPanel = lazy(() =>
  import("./panels/OverviewPanel").then((m) => ({ default: m.OverviewPanel }))
);
import type {
  ChannelNow,
  ChannelSummary,
  PackageProfile,
} from "./types";

const SCHEDULE_WARN_HOURS = 6;
const ADMIN_PANEL_IDS = new Set(["overview", "guide", "library", "sources", "tools", "encoding", "profiles", "schedule"]);
const SIDEBAR_AUTO_CLOSE_QUERY = "(max-width: 900px)";
const NON_ERROR_STATUS_PREFIXES = [
  "queueing package run",
  "queued packages",
  "enabled",
  "disabled",
  "cleared",
  "duplicated",
  "duplicating",
  "hidden",
  "hiding",
  "inserted",
  "skipped",
  "showing",
  "policy",
  "profile",
  "resetting",
  "saving",
  "visible",
  "artwork",
];

// ---------------------------------------------------------------------------
// AdminPage root
// ---------------------------------------------------------------------------

export function AdminPage() {
  const [authState, setAuthState] = useState<"checking" | "authenticated" | "must-change" | "unauthenticated" | "unavailable">("checking");
  const [authEnabled, setAuthEnabled] = useState(false);
  const [authError, setAuthError] = useState("");

  useEffect(() => {
    let cancelled = false;
    getAdminAuthStatus()
      .then((status) => {
        if (cancelled) return;
        setAuthEnabled(status.enabled);
        setAuthError("");
        if (status.authenticated) {
          setAuthState(status.mustChange ? "must-change" : "authenticated");
        } else {
          setAuthState("unauthenticated");
        }
      })
      .catch((err) => {
        if (cancelled) return;
        setAuthError(err instanceof Error ? err.message : String(err));
        setAuthState("unavailable");
      });
    return () => {
      cancelled = true;
    };
  }, []);

  // A 401 on any non-auth request means the session lapsed (commonly after a
  // redeploy restarts the admin). Drop back to the login screen rather than
  // leaving the operator on a workspace whose data calls all fail.
  useEffect(() => {
    function handleUnauthorized() {
      setAuthEnabled(true);
      setAuthState((prev) => (prev === "authenticated" || prev === "must-change" ? "unauthenticated" : prev));
    }
    window.addEventListener(UNAUTHORIZED_EVENT, handleUnauthorized);
    return () => window.removeEventListener(UNAUTHORIZED_EVENT, handleUnauthorized);
  }, []);

  if (authState === "checking") {
    return (
      <div className="admin-auth-page">
        <div className="admin-auth-panel">
          <h1>Admin</h1>
          <p className="muted">checking session...</p>
        </div>
      </div>
    );
  }

  if (authState === "unavailable") {
    return <AdminUnavailable error={authError} />;
  }

  if (authState === "unauthenticated") {
    return (
      <AuthLoginForm
        title="Admin"
        authEnabled={authEnabled}
        onAuthenticated={(mustChange) =>
          setAuthState(mustChange ? "must-change" : "authenticated")
        }
        disabledNotice={
          <p className="muted">
            Admin auth is disabled. Did you mean to enable no-auth mode? Use
            LINEARCAST_ADMIN_ALLOW_NO_AUTH=true only for development or recovery.
          </p>
        }
      />
    );
  }

  if (authState === "must-change") {
    return (
      <ChangePasswordForm
        onChanged={() => setAuthState("authenticated")}
      />
    );
  }

  return (
    <AdminWorkspace
      authEnabled={authEnabled}
      onLogout={() => {
        setAuthState("unauthenticated");
        setAuthEnabled(true);
      }}
    />
  );
}

function AdminUnavailable({ error }: { error: string }) {
  return (
    <div className="admin-auth-page">
      <div className="admin-auth-panel">
        <h1>Admin</h1>
        <p className="muted">
          Admin API is unavailable. If auth is enabled, sign in with the
          default password and change it, or enable no-auth mode with
          LINEARCAST_ADMIN_ALLOW_NO_AUTH=true for development or recovery.
        </p>
        {error && <p className="form-status error">{error}</p>}
        <Link to="/" className="admin-auth-live-link">Back to live</Link>
      </div>
    </div>
  );
}

function AdminWorkspace({
  authEnabled,
  onLogout,
}: {
  authEnabled: boolean;
  onLogout: () => void;
}) {
  const { data: adminNow } = useAdminNow(15000);
  const { channels: allChannels, loaded: channelsLoaded, refresh: refreshChannels } = useChannelList(60000);

  const [selected, setSelected] = useState<string>("overview"); // channelID or an ADMIN_PANEL_IDS value
  // Once the Inventory panel has been opened we keep it mounted (hidden when
  // another view is active) so returning to it is instant and doesn't re-run
  // its inventory query.
  const [libraryVisited, setLibraryVisited] = useState(false);
  useEffect(() => {
    if (selected === "library") setLibraryVisited(true);
  }, [selected]);
  const [sidebarOpen, setSidebarOpen] = useState(() => {
    if (typeof window === "undefined") return true;
    return !window.matchMedia(SIDEBAR_AUTO_CLOSE_QUERY).matches;
  });
  const [allowedProfiles, setAllowedProfiles] = useState<string[]>([]);
  const [profileDetails, setProfileDetails] = useState<Record<string, PackageProfile>>({});
  const [deleteTarget, setDeleteTarget] = useState<ChannelSummary | null>(null);
  // When the schedule panel is open in edit mode this holds the channel to
  // preload; null means the panel is building a brand-new channel.
  const [scheduleChannelId, setScheduleChannelId] = useState<string | null>(null);
  // Keep a fresh-channel draft mounted while the operator checks another admin
  // panel. Existing-channel editing remains ephemeral and separate so it can
  // never overwrite the preserved fresh draft.
  const [freshScheduleVisited, setFreshScheduleVisited] = useState(false);
  const [freshScheduleKey, setFreshScheduleKey] = useState(0);
  const {
    rowBusy,
    rowStatus,
    extendSchedule,
    clearSchedule,
    restartPlayback,
    setEnabled,
    setHiddenFromGuide,
    deleteChannel,
    cloneChannel,
    updateArtwork,
    changeOnDemandProfile,
  } = useChannelActions({
    allowedProfiles,
    profileDetails,
    refreshChannels,
    selected,
    setSelected,
  });

  const enabledChannelIds = new Set(allChannels.filter((c) => c.enabled).map((c) => c.id));
  const enabledChannels = (adminNow?.channels ?? []).filter((c) =>
    channelsLoaded ? enabledChannelIds.has(c.id) : true,
  );
  const disabledChannels = allChannels.filter((c) => !c.enabled);

  const selectedEnabled = enabledChannels.find((c) => c.id === selected) ?? null;
  const selectedDisabled = disabledChannels.find((c) => c.id === selected) ?? null;

  function closeDeleteDialog() {
    setDeleteTarget(null);
  }

  function confirmDeleteChannel(reclaimEncodes: boolean) {
    if (!deleteTarget) return;
    const target = deleteTarget;
    setDeleteTarget(null);
    void deleteChannel(target.id, target.displayName, reclaimEncodes, target.enabled);
  }

  // Auto-load allowed profiles on mount.
  useEffect(() => {
    getMediaPackageProfileList()
      .then((next) => {
        if (next.profiles.length > 0) setAllowedProfiles(next.profiles);
        setProfileDetails(Object.fromEntries(next.profileDetails.map((item) => [item.name, item])));
      })
      .catch(() => {});
  }, []);

  // Keep the navigation out of the content area once the layout becomes narrow.
  useEffect(() => {
    const query = window.matchMedia(SIDEBAR_AUTO_CLOSE_QUERY);
    const onChange = () => {
      if (query.matches) setSidebarOpen(false);
    };
    onChange();
    query.addEventListener("change", onChange);
    return () => query.removeEventListener("change", onChange);
  }, []);

  // ---------------------------------------------------------------------------
  // Render
  // ---------------------------------------------------------------------------

  // Channels with a non-empty error status message.
  const errorIds = new Set(
    Object.entries(rowStatus)
      .filter(([, msg]) => msg && !NON_ERROR_STATUS_PREFIXES.some((prefix) => msg.startsWith(prefix)))
      .map(([id]) => id),
  );

  function selectChannel(id: string) {
    setSelected(id);
    // On narrow layouts, close the sidebar after selecting a view.
    if (window.matchMedia(SIDEBAR_AUTO_CLOSE_QUERY).matches) setSidebarOpen(false);
  }

  // Opening an admin panel by id. The schedule builder needs its draft state
  // reset to "brand new channel" first, so every entry point into it — the
  // sidebar and the overview's empty state — goes through here.
  function openPanel(panel: string) {
    if (panel === "schedule") {
      setScheduleChannelId(null);
      setFreshScheduleVisited(true);
    }
    selectChannel(panel);
  }

  async function logout() {
    await logoutAdmin().catch(() => {});
    onLogout();
  }

  return (
    <div className="admin-page">
      <header className="admin-page-header">
        <div className="admin-page-brand">
          <button
            type="button"
            className="admin-sidebar-toggle"
            aria-label={sidebarOpen ? "Close sidebar" : "Open sidebar"}
            onClick={() => setSidebarOpen((v) => !v)}
          >
            ☰
          </button>
          <Link to="/" className="admin-back-link">
            ← Live
          </Link>
          <span className="admin-page-title">Admin</span>
        </div>
        <div className="admin-page-session">
          {authEnabled && (
            <button type="button" className="admin-logout-btn" onClick={() => void logout()}>
              Log out
            </button>
          )}
        </div>
      </header>

      <div className="admin-page-body">
        {/* Sidebar */}
        <nav className={`admin-sidebar${sidebarOpen ? "" : " is-collapsed"}`} aria-hidden={!sidebarOpen}>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "overview" ? " is-selected" : ""}`}
            onClick={() => selectChannel("overview")}
          >
            Overview
          </button>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "guide" ? " is-selected" : ""}`}
            onClick={() => selectChannel("guide")}
          >
            Guide
          </button>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "library" ? " is-selected" : ""}`}
            onClick={() => selectChannel("library")}
          >
            Inventory
          </button>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "sources" ? " is-selected" : ""}`}
            onClick={() => selectChannel("sources")}
          >
            Sources
          </button>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "tools" ? " is-selected" : ""}`}
            onClick={() => selectChannel("tools")}
          >
            Tools
          </button>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "profiles" ? " is-selected" : ""}`}
            onClick={() => selectChannel("profiles")}
          >
            Profiles
          </button>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "encoding" ? " is-selected" : ""}`}
            onClick={() => selectChannel("encoding")}
          >
            Encoding
          </button>
          <button
            type="button"
            className={`admin-sidebar-item${selected === "schedule" ? " is-selected" : ""}`}
            onClick={() => openPanel("schedule")}
          >
            Create channel
          </button>

          {enabledChannels.length > 0 && (
            <div className="admin-sidebar-label">Enabled</div>
          )}
          {enabledChannels.map((ch) => (
            <button
              key={ch.id}
              type="button"
              className={`admin-sidebar-item${selected === ch.id ? " is-selected" : ""}`}
              onClick={() => selectChannel(ch.id)}
            >
              <span className={`sidebar-dot status-dot-${ch.status}`} />
              <span className="sidebar-name">{ch.displayName || ch.id}</span>
              {ch.hiddenFromGuide && <span className="sidebar-pill">hidden</span>}
              {errorIds.has(ch.id) && <span className="sidebar-error-dot" aria-label="error" />}
            </button>
          ))}

          {disabledChannels.length > 0 && (
            <div className="admin-sidebar-label">Disabled</div>
          )}
          {disabledChannels.map((ch) => (
            <button
              key={ch.id}
              type="button"
              className={`admin-sidebar-item is-disabled${selected === ch.id ? " is-selected" : ""}`}
              onClick={() => selectChannel(ch.id)}
            >
              <span className="sidebar-dot" />
              <span className="sidebar-name">{ch.displayName || ch.id}</span>
              {ch.hiddenFromGuide && <span className="sidebar-pill">hidden</span>}
              {errorIds.has(ch.id) && <span className="sidebar-error-dot" aria-label="error" />}
            </button>
          ))}
        </nav>

        {/* Main panel */}
        <main className="admin-main">
          <Suspense fallback={<div className="admin-panel"><section className="admin-panel-section"><p className="muted">loading…</p></section></div>}>
          {selected === "overview" && (
            <OverviewPanel
              channels={enabledChannels}
              loaded={adminNow !== null && channelsLoaded}
              disabledCount={disabledChannels.length}
              busy={rowBusy}
              status={rowStatus}
              onSelectChannel={selectChannel}
              onOpenPanel={openPanel}
              onExtend={(id, hours) => void extendSchedule(id, hours)}
            />
          )}

          {selected === "guide" && (
            <div className="admin-panel">
              <section className="admin-panel-section">
                <div className="section-headline">
                  <div className="section-headline-main">
                    <h2>Guide</h2>
                    <p className="section-purpose">
                      Live program grid across all channels. Click a program to open its channel.
                    </p>
                  </div>
                </div>
                <ChannelGuide activeChannelID={null} onSelect={selectChannel} />
              </section>
            </div>
          )}

			{selected === "tools" && <ToolsPanel />}

          {libraryVisited && (
            <div style={{ display: selected === "library" ? undefined : "none" }}>
              <InventoryPanel />
            </div>
          )}

          {selected === "sources" && (
            <div className="admin-panel">
              <MediaSourcesPanel />
            </div>
          )}

          {selected === "encoding" && <EncodingPanel />}

          {selected === "profiles" && <ProfilesPanel />}

          {freshScheduleVisited && (
            <div style={{ display: selected === "schedule" && scheduleChannelId === null ? undefined : "none" }}>
              <ScheduleBuilderPanel
                key={freshScheduleKey}
                active={selected === "schedule" && scheduleChannelId === null}
                onChannelImported={() => {
                  refreshChannels();
                  setFreshScheduleKey((current) => current + 1);
                  selectChannel("library");
                }}
                onOpenMediaSources={() => selectChannel("sources")}
              />
            </div>
          )}

          {selected === "schedule" && scheduleChannelId && (
            <ScheduleBuilderPanel
              existingChannel={enabledChannels.find((c) => c.id === scheduleChannelId)}
              onChannelImported={() => {
                refreshChannels();
                const target = scheduleChannelId;
                setScheduleChannelId(null);
                selectChannel(target);
              }}
              onOpenMediaSources={() => selectChannel("sources")}
            />
          )}

          {selectedEnabled && (
            <ChannelPanel
              channel={selectedEnabled}
              busy={rowBusy[selectedEnabled.id] ?? false}
              status={rowStatus[selectedEnabled.id] ?? ""}
              onChangeProfile={() =>
                void changeOnDemandProfile(
                  selectedEnabled.id,
                  selectedEnabled.displayName,
                  selectedEnabled.packageProfile,
                  selectedEnabled.mediaKind,
                )
              }
              onExtend={(hours) => void extendSchedule(selectedEnabled.id, hours)}
              onRestart={() => void restartPlayback(selectedEnabled.id, selectedEnabled.displayName)}
              onClearSchedule={() => void clearSchedule(selectedEnabled.id, selectedEnabled.displayName)}
              onClone={() => void cloneChannel(selectedEnabled.id, selectedEnabled.displayName)}
              onUpdateArtwork={() =>
                void updateArtwork(
                  selectedEnabled.id,
                  selectedEnabled.displayName,
                  selectedEnabled.artworkUrl,
                )
              }
              onEditSchedule={() => {
                setScheduleChannelId(selectedEnabled.id);
                selectChannel("schedule");
              }}
              onHiddenFromGuideChange={(hidden) =>
                void setHiddenFromGuide(selectedEnabled.id, selectedEnabled.displayName, hidden)
              }
              onDisable={() => void setEnabled(selectedEnabled.id, selectedEnabled.displayName, false)}
              onDelete={() => {
                const summary = allChannels.find((c) => c.id === selectedEnabled.id);
                if (summary) setDeleteTarget(summary);
              }}
            />
          )}

          {selectedDisabled && (
            <DisabledChannelPanel
              channel={selectedDisabled}
              busy={rowBusy[selectedDisabled.id] ?? false}
              status={rowStatus[selectedDisabled.id] ?? ""}
              onEnable={() => void setEnabled(selectedDisabled.id, selectedDisabled.displayName, true)}
              onDelete={() => setDeleteTarget(selectedDisabled)}
            />
          )}
          </Suspense>
        </main>
      </div>
      <Dialog
        open={deleteTarget !== null}
        onClose={closeDeleteDialog}
        title={`Delete ${deleteTarget?.displayName || deleteTarget?.id || "channel"}`}
      >
        <div className="delete-channel-dialog">
          <p>
            Delete this channel, its playlist membership, and its schedule? An
            enabled channel is disabled first, so it drops from the grid. Source
            media stays in the library. You can also reclaim packaged encodes
            that are no longer used by another channel.
          </p>
          <div className="delete-channel-dialog-actions">
            <button type="button" className="link-button" onClick={closeDeleteDialog}>
              Cancel
            </button>
            <button type="button" onClick={() => confirmDeleteChannel(false)}>
              Delete channel, keep encodes
            </button>
            <button type="button" className="danger" onClick={() => confirmDeleteChannel(true)}>
              Delete channel and reclaim encodes
            </button>
          </div>
          <p className="muted delete-channel-dialog-note">
            Shared media is skipped; this never deletes source media files.
          </p>
        </div>
      </Dialog>
    </div>
  );
}

// ---------------------------------------------------------------------------
// ChannelPanel
// ---------------------------------------------------------------------------

function ChannelPanel({
  channel,
  busy,
  status,
  onChangeProfile,
  onExtend,
  onRestart,
  onClearSchedule,
  onClone,
  onUpdateArtwork,
  onEditSchedule,
  onHiddenFromGuideChange,
  onDisable,
  onDelete,
}: {
  channel: ChannelNow;
  busy: boolean;
  status: string;
  onChangeProfile: () => void;
  onExtend: (hours?: number) => void;
  onRestart: () => void;
  onClearSchedule: () => void;
  onClone: () => void;
  onUpdateArtwork: () => void;
  onEditSchedule: () => void;
  onHiddenFromGuideChange: (hidden: boolean) => void;
  onDisable: () => void;
  onDelete: () => void;
}) {
	const [extendHours, setExtendHours] = useState("24");
  const schedHrs = channel.scheduleCoverageHours ?? 0;
  const schedWarn = schedHrs < SCHEDULE_WARN_HOURS;
  const pkgWarn = (channel.packageCoverageMs ?? 0) === 0;
  const isLiveEncoded = channel.prefillMode === "on_demand";

  return (
    <div className="admin-panel">
      {/* Header */}
      <section className="admin-panel-section channel-panel-header">
        <ChannelArtwork
          artworkUrl={channel.artworkUrl}
          channelId={channel.id}
          displayName={channel.displayName}
          className="channel-panel-artwork"
        />
        <div className="channel-panel-title">
          <h2>{channel.displayName || channel.id}</h2>
          <span className={`status status-${channel.status}`}>{channel.status}</span>
          {channel.hiddenFromGuide && <span className="status">hidden</span>}
        </div>
        <div className="channel-panel-coverage">
			<span className={schedWarn ? "danger" : ""}>
				schedule {formatMs(channel.scheduleCoverageMs)}
			</span>
			<span className="muted">·</span>
			<span className={pkgWarn ? "danger" : ""}>
				{channel.packageReadyCount} items ({formatMs(channel.packageCoverageMs)})
			</span>
			<span className="muted">·</span>
			<span className="muted">{channel.packageProfile}</span>
          <span className="muted">·</span>
          <span className="muted">{channel.mediaKind}</span>
        </div>
      </section>

      {/* Actions */}
      <section className="admin-panel-section">
        <h3>Actions</h3>
        <div className="channel-actions">
			<div className="channel-action-extend">
                <button
                  type="button"
                  disabled={busy}
                  onClick={() => onExtend(Number(extendHours) || undefined)}
                  title="Append ready packaged media to the schedule without clearing it."
                >
                  {busy ? "…" : "Extend schedule"}
                </button>
                <label className="channel-extend-hours">
                  <input
                    type="number"
                    min="1"
                    step="1"
                    value={extendHours}
                    disabled={busy}
                    onChange={(e) => setExtendHours(e.target.value)}
                    title="Hours to extend"
                  />
                  <span className="muted">h</span>
                </label>
			</div>
			<button type="button" disabled={busy} onClick={onRestart} title="Clear the schedule and rebuild from ready packages now.">
				{busy ? "…" : "Remake schedule"}
			</button>
          <button type="button" disabled={busy} onClick={onClone}>
            {busy ? "…" : "Duplicate channel"}
          </button>
			{isLiveEncoded && (
            <button type="button" disabled={busy} onClick={onChangeProfile}>
              {busy ? "…" : "Change profile"}
            </button>
          )}
			<div className="channel-action-artwork">
              <button type="button" disabled={busy} onClick={onUpdateArtwork}>
                {busy ? "…" : channel.artworkUrl ? "Update artwork" : "Set artwork"}
              </button>
			</div>
          <button
            type="button"
            disabled={busy}
            onClick={() => onHiddenFromGuideChange(!channel.hiddenFromGuide)}
          >
            {busy ? "…" : channel.hiddenFromGuide ? "Show in guide" : "Hide from guide"}
          </button>
			<button type="button" className="danger" disabled={busy} onClick={onClearSchedule}>
				{busy ? "…" : "Clear schedule"}
			</button>
          <button type="button" className="danger" disabled={busy} onClick={onDisable}>
            {busy ? "…" : "Disable channel"}
          </button>
          <button type="button" className="danger" disabled={busy} onClick={onDelete}>
            {busy ? "…" : "Delete channel"}
          </button>
        </div>
        {status && <p className="channel-status-msg muted">{status}</p>}
      </section>

		{/* Schedule */}
		<section className="admin-panel-section">
            <div className="section-headline">
              <h3>Schedule</h3>
              <button type="button" className="link-button" onClick={onEditSchedule}>
                Edit schedule
              </button>
            </div>
            <p className="muted">
              Opens the schedule builder with this channel preloaded.
            </p>
		</section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// DisabledChannelPanel
// ---------------------------------------------------------------------------

function DisabledChannelPanel({
  channel,
  busy,
  status,
  onEnable,
  onDelete,
}: {
  channel: ChannelSummary;
  busy: boolean;
  status: string;
  onEnable: () => void;
  onDelete: () => void;
}) {
  return (
    <div className="admin-panel">
      <section className="admin-panel-section channel-panel-header">
        <ChannelArtwork
          artworkUrl={channel.artworkUrl}
          channelId={channel.id}
          displayName={channel.displayName}
          className="channel-panel-artwork"
        />
        <div className="channel-panel-title">
          <h2>{channel.displayName || channel.id}</h2>
          <span className="status">disabled</span>
          {channel.hiddenFromGuide && <span className="status">hidden</span>}
        </div>
        <span className="muted channel-panel-id">{channel.id}</span>
      </section>
      <section className="admin-panel-section">
        <h3>Actions</h3>
        <div className="channel-actions">
          <button type="button" className="primary" disabled={busy} onClick={onEnable}>
            {busy ? "…" : "Enable channel"}
          </button>
          <button type="button" className="danger" disabled={busy} onClick={onDelete}>
            {busy ? "…" : "Delete channel"}
          </button>
        </div>
        {status && <p className="channel-status-msg muted">{status}</p>}
      </section>
    </div>
  );
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------
