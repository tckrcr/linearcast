import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import {
  cancelMediaPackages,
  deleteEncoder,
  encoderDownloadURL,
  getEncoderDownloads,
  getEncoders,
  getMediaPackageCandidates,
  getMediaPackageProfileList,
  registerEncoder,
  requestMediaPackages,
  revokeEncoder,
  updateEncoderConcurrency,
  updateLocalWorker,
} from "../api";
import { Dialog } from "../Dialog";
import { formatMs } from "../format";
import { usePolling } from "../hooks/usePolling";
import type {
  EncoderDownloadsResponse,
  EncoderListItem,
  EncoderRegisterResponse,
  EncoderDownloadEntry,
  LocalWorkerItem,
  MediaPackageCandidateList,
  MediaPackageRequestResult,
  PackageProfile,
} from "../types";
import styles from "./EncodingPanel.module.css";

const ALL_PROFILES = "all";

function isABRProfile(profile: PackageProfile | undefined): boolean {
  return (profile?.tags ?? []).includes("abr");
}
type EncoderPlatform = "darwin-arm64" | "darwin-amd64" | "windows-amd64" | "linux-amd64" | "linux-arm64";

const ENCODER_PLATFORM_OPTIONS: Array<{ platform: EncoderPlatform; label: string }> = [
  { platform: "darwin-arm64", label: "macOS (Apple Silicon)" },
  { platform: "darwin-amd64", label: "macOS (Intel)" },
  { platform: "windows-amd64", label: "Windows" },
  { platform: "linux-amd64", label: "Linux (x86_64)" },
  { platform: "linux-arm64", label: "Linux (ARM64)" },
];

const PRIMARY_ENCODER_DOWNLOADS: Array<{ platform: EncoderPlatform; label: string }> = [
  { platform: "darwin-arm64", label: "macOS" },
  { platform: "windows-amd64", label: "Windows" },
  { platform: "linux-amd64", label: "Linux x86" },
];

function isRemoteEncoder(e: EncoderListItem | LocalWorkerItem): e is EncoderListItem {
  return e.id !== "local";
}

function ConcurrencyCell({
  value,
  min,
  onSave,
}: {
  value: number;
  min: number;
  onSave: (raw: string) => void;
}) {
  const [text, setText] = useState(String(value));
  useEffect(() => {
    setText(String(value));
  }, [value]);
  return (
    <input
      className={styles["encoder-concurrency-input"]}
      type="number"
      min={min}
      value={text}
      onChange={(e) => setText(e.target.value)}
      onBlur={() => {
        if (text === String(value)) return;
        onSave(text);
      }}
      onKeyDown={(e) => {
        if (e.key === "Enter") (e.target as HTMLInputElement).blur();
      }}
    />
  );
}

export function EncodingPanel() {
  const [profile, setProfile] = useState("");
  const [profiles, setProfiles] = useState<string[]>([]);
  const [profileDetails, setProfileDetails] = useState<Record<string, PackageProfile>>({});
  const [filter, setFilter] = useState("");
  const [debouncedFilter, setDebouncedFilter] = useState("");
  const [statusFilter, setStatusFilter] = useState("");
  const [data, setData] = useState<MediaPackageCandidateList | null>(null);
  const [offset, setOffset] = useState(0);
  const [selectedIds, setSelectedIds] = useState<Set<string>>(new Set());
  const [loading, setLoading] = useState(false);
  const [loadingMore, setLoadingMore] = useState(false);
  const [submitBusy, setSubmitBusy] = useState(false);
  const [cancelBusy, setCancelBusy] = useState(false);
  const [status, setStatus] = useState("");
  const [lastResult, setLastResult] = useState<MediaPackageRequestResult | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [encoders, setEncoders] = useState<Array<EncoderListItem | LocalWorkerItem>>([]);
  const [encoderName, setEncoderName] = useState("");
  const [encoderBusy, setEncoderBusy] = useState(false);
  const [encoderStatus, setEncoderStatus] = useState("");
  const [newEncoder, setNewEncoder] = useState<EncoderRegisterResponse | null>(null);
  const [downloads, setDownloads] = useState<EncoderDownloadsResponse | null>(null);
  const [downloadsErr, setDownloadsErr] = useState("");
  const [selectedDownloadPlatform, setSelectedDownloadPlatform] = useState<EncoderPlatform>(() => defaultPrimaryPlatform());
  const [showRevoked, setShowRevoked] = useState(false);
  useEffect(() => {
    const id = window.setInterval(() => setNow(Date.now()), 1000);
    return () => window.clearInterval(id);
  }, []);

  useEffect(() => {
    const id = window.setTimeout(() => setDebouncedFilter(filter), 300);
    return () => window.clearTimeout(id);
  }, [filter]);

  const loadProfiles = useCallback(() => {
    getMediaPackageProfileList()
      .then((next) => {
        if (next.profiles.length === 0) return;
        const details = Object.fromEntries(next.profileDetails.map((item) => [item.name, item]));
        const visible = next.profiles.filter((p) => !isABRProfile(details[p]));
        setProfiles(visible);
        setProfileDetails(details);
        setProfile((current) => current === ALL_PROFILES || visible.includes(current) ? current : next.defaultProfile || visible[0]);
      })
      .catch((err) => {
        setStatus(err instanceof Error ? err.message : String(err));
      });
  }, []);

  const loadCandidates = useCallback(
    async (silent = false, signal?: AbortSignal) => {
      if (!silent) setLoading(true);
      try {
        const next = await getMediaPackageCandidates(
          profile.trim(),
          debouncedFilter.trim() || undefined,
          statusFilter || undefined,
          undefined,
          signal,
        );
        setOffset(0);
        setData(next);
        setSelectedIds((prev) => {
          const selectable = new Set(next.media.filter((m) => m.selectable).map((m) => m.mediaId));
          const kept = new Set<string>();
          prev.forEach((id) => {
            if (selectable.has(id)) kept.add(id);
          });
          return kept;
        });
        if (!silent) setStatus("");
      } catch (err) {
        if (signal?.aborted) return;
        setStatus(err instanceof Error ? err.message : String(err));
        throw err;
      } finally {
        if (!signal?.aborted) setLoading(false);
      }
    },
    [profile, debouncedFilter, statusFilter],
  );

  const loadMore = useCallback(() => {
    const nextOffset = offset + 100;
    setLoadingMore(true);
    getMediaPackageCandidates(
      profile.trim(),
      debouncedFilter.trim() || undefined,
      statusFilter || undefined,
      nextOffset,
    )
      .then((next) => {
        setOffset(nextOffset);
        setData((prev) => {
          if (!prev) return next;
          const existingIds = new Set(prev.media.map((m) => m.mediaId));
          const newMedia = next.media.filter((m) => !existingIds.has(m.mediaId));
          return { ...prev, media: [...prev.media, ...newMedia] };
        });
        setSelectedIds((prev) => {
          const selectable = new Set(next.media.filter((m) => m.selectable).map((m) => m.mediaId));
          const next2 = new Set(prev);
          selectable.forEach((id) => {
            if (!prev.has(id)) next2.delete(id);
          });
          return next2;
        });
      })
      .catch((err) => setStatus(err instanceof Error ? err.message : String(err)))
      .finally(() => setLoadingMore(false));
  }, [profile, debouncedFilter, statusFilter, offset]);

  useEffect(() => loadProfiles(), [loadProfiles]);
  useEffect(() => { void loadCandidates(false); }, [loadCandidates]);
  usePolling({
    intervalMs: 5000,
    maxIntervalMs: 60_000,
    task: (signal) => loadCandidates(true, signal),
  });

  const loadEncoders = useCallback(async (silent = false, signal?: AbortSignal) => {
    try {
      const next = await getEncoders(signal);
      const list: Array<EncoderListItem | LocalWorkerItem> = [];
      if (next.localWorker) {
        list.push(next.localWorker);
      }
      list.push(...(next.encoders ?? []));
      setEncoders(list);
      if (!silent) setEncoderStatus("");
    } catch (err) {
      if (signal?.aborted) return;
      if (!silent) setEncoderStatus(err instanceof Error ? err.message : String(err));
      throw err;
    }
  }, []);

  useEffect(() => { void loadEncoders(false); }, [loadEncoders]);
  usePolling({
    intervalMs: 10_000,
    maxIntervalMs: 60_000,
    task: (signal) => loadEncoders(true, signal),
  });

  const loadEncoderDownloads = useCallback(() => {
    setDownloadsErr("");
    return getEncoderDownloads()
      .then(setDownloads)
      .catch((err) => setDownloadsErr(err instanceof Error ? err.message : String(err)));
  }, []);

  useEffect(() => {
    void loadEncoderDownloads();
  }, [loadEncoderDownloads]);

  const revokedCount = encoders.filter((e) => isRemoteEncoder(e) && e.revokedAtMs).length;
  const visibleEncoders = showRevoked ? encoders : encoders.filter((e) => !(isRemoteEncoder(e) && e.revokedAtMs));

  async function saveEncoderConcurrency(id: string, isLocal: boolean, raw: string) {
    const n = parseInt(raw, 10);
    if (!Number.isFinite(n) || n < (isLocal ? 0 : 1)) {
      setEncoderStatus(`concurrency must be ${isLocal ? ">= 0" : ">= 1"}`);
      return;
    }
    try {
      if (isLocal) {
        await updateLocalWorker({ concurrency: n });
      } else {
        await updateEncoderConcurrency(id, n);
      }
      setEncoderStatus("");
      loadEncoders(true);
    } catch (err) {
      setEncoderStatus(err instanceof Error ? err.message : String(err));
    }
  }

  async function toggleLocalWorker(currentConcurrency: number) {
    // enabled = concurrency > 0; toggling sets 0 (disable) or 1 (enable).
    const concurrency = currentConcurrency > 0 ? 0 : 1;
    try {
      await updateLocalWorker({ concurrency });
      setEncoderStatus("");
      loadEncoders(true);
    } catch (err) {
      setEncoderStatus(err instanceof Error ? err.message : String(err));
    }
  }

  const rows = data?.media ?? [];
  const activeJobs = rows.filter((m) => m.packageStatus === "processing");
  const visibleRows = rows;
  const selectableRows = visibleRows.filter((m) => m.selectable);
  const selectedCount = selectedIds.size;
  const countLabel = candidateCountLabel(statusFilter);
  const counts = (data?.statusCounts ?? []).reduce<Record<string, number>>((acc, row) => {
    acc[row.status] = row.count;
    return acc;
  }, {});
  const allSelectableChecked = selectableRows.length > 0 && selectableRows.every((m) => selectedIds.has(m.mediaId));

  function toggleMedia(mediaId: string, checked: boolean) {
    setSelectedIds((prev) => {
      const next = new Set(prev);
      if (checked) next.add(mediaId);
      else next.delete(mediaId);
      return next;
    });
  }

  function toggleAllSelectable(checked: boolean) {
    if (!checked) {
      setSelectedIds(new Set());
      return;
    }
    setSelectedIds(new Set(selectableRows.map((m) => m.mediaId)));
  }

  async function submitSelected() {
    const ids = Array.from(selectedIds);
    if (ids.length === 0 || profile === ALL_PROFILES) return;
    setSubmitBusy(true);
    setStatus(`queueing ${ids.length} item${ids.length === 1 ? "" : "s"}…`);
    try {
      const result = await requestMediaPackages(ids, profile.trim());
      setLastResult(result);
      setSelectedIds(new Set());
      setStatus(`queued ${result.queued.length}, already queued ${result.alreadyPending.length}, ready ${result.alreadyReady.length}, failed ${result.failed.length}`);
      loadCandidates(true);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setSubmitBusy(false);
    }
  }

  async function cancelQueuedAndEncoding() {
    const pending = counts.pending ?? 0;
    const processing = counts.processing ?? 0;
    const total = pending + processing;
    if (total === 0 || cancelBusy) return;
    const scope = profile === ALL_PROFILES ? "all profiles" : profile;
    if (!window.confirm(`Cancel ${total} queued/encoding package job${total === 1 ? "" : "s"} for ${scope}?`)) return;
    setCancelBusy(true);
    setStatus(`cancelling ${total} package job${total === 1 ? "" : "s"}…`);
    try {
      const result = await cancelMediaPackages({
        profile: profile.trim() || undefined,
        all: true,
      });
      setSelectedIds(new Set());
      setStatus(`cancelled ${result.canceledPending} queued and ${result.canceledProcessing} encoding job${result.canceledPending + result.canceledProcessing === 1 ? "" : "s"}`);
      loadCandidates(true);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setCancelBusy(false);
    }
  }

  async function submitEncoder(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    const name = encoderName.trim();
    if (!name || encoderBusy) return;
    setEncoderBusy(true);
    setEncoderStatus("registering encoder...");
    setNewEncoder(null);
    setDownloadsErr("");
    try {
      const result = await registerEncoder(name);
      setNewEncoder(result);
      setEncoderName("");
      setEncoderStatus(`registered ${result.name}`);
      loadEncoders(true);
      void loadEncoderDownloads();
    } catch (err) {
      setEncoderStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setEncoderBusy(false);
    }
  }

  async function deleteRow(id: string, name: string) {
    if (encoderBusy) return;
    if (!window.confirm(`Delete encoder ${name}? Any in-flight encode held by this encoder will be released back to the queue.`)) return;
    setEncoderBusy(true);
    setEncoderStatus(`deleting ${name}...`);
    try {
      await deleteEncoder(id);
      setEncoderStatus(`deleted ${name}`);
      loadEncoders(true);
    } catch (err) {
      setEncoderStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setEncoderBusy(false);
    }
  }

  async function revoke(id: string, name: string) {
    if (encoderBusy) return;
    if (!window.confirm(`Revoke this API key? Packages already encoded by ${name} keep their attribution.`)) return;
    setEncoderBusy(true);
    setEncoderStatus(`revoking ${name}...`);
    try {
      await revokeEncoder(id);
      setEncoderStatus(`revoked ${name}`);
      loadEncoders(true);
    } catch (err) {
      setEncoderStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setEncoderBusy(false);
    }
  }

  return (
    <div className="admin-panel encoding-panel">
      <section className="admin-panel-section encoder-admin-section">
        <div className="section-headline">
          <h2>Encoders</h2>
          <button type="button" disabled={encoderBusy} onClick={() => loadEncoders(false)}>
            refresh
          </button>
        </div>
        <div className={styles["encoder-register-row"]}>
          <form className={styles["encoder-register-form"]} onSubmit={(event) => void submitEncoder(event)}>
            <label>
              <span>new remote encoder name</span>
              <input
                value={encoderName}
                placeholder="nvidia-gpu, apple-videotoolbox..."
                onChange={(event) => setEncoderName(event.target.value)}
              />
            </label>
            <button type="submit" disabled={encoderBusy || encoderName.trim() === ""}>
              {encoderBusy ? "working..." : "Register remote encoder"}
            </button>
          </form>
          <EncoderDownloadControl
            downloads={downloads}
            downloadsError={downloadsErr}
            selectedPlatform={selectedDownloadPlatform}
            onSelect={setSelectedDownloadPlatform}
          />
        </div>
        <EncoderRegisteredDialog
          encoder={newEncoder}
          downloads={downloads}
          downloadsError={downloadsErr}
          onClose={() => {
            setNewEncoder(null);
          }}
        />
        <table className={styles["encoder-table"]}>
          <thead>
            <tr>
              <th>encoder</th>
              <th>status</th>
              <th>concurrency</th>
              <th>current job</th>
              <th>progress</th>
              <th>host</th>
              <th>system</th>
              <th>gpu</th>
              <th>disk</th>
              <th>ip</th>
              <th>last seen</th>
              <th />
            </tr>
          </thead>
          <tbody>
            {visibleEncoders.map((encoder) => {
              const details = encoderDetails(encoder);
              const jobs = encoder.jobs ?? [];
              const job = jobs[0];
              const isLocal = !isRemoteEncoder(encoder);
              const revokedAt = isRemoteEncoder(encoder) ? encoder.revokedAtMs : undefined;
              return (
                <tr key={encoder.id} className={revokedAt ? "is-revoked" : ""}>
                  <td>
                    <span className={styles["encoder-name"]}>{encoder.name}</span>
                    {!isLocal && <span className={`muted ${styles["encoder-id"]}`}>{encoder.id}</span>}
                  </td>
                  <td>
                    <span className={`episode-pkg ${encoderBadgeClass(encoder, now)}`}>
                      {encoderBadgeLabel(encoder, now)}
                    </span>
                  </td>
                  <td>
                    <ConcurrencyCell
                      value={encoder.concurrency ?? 1}
                      min={isLocal ? 0 : 1}
                      onSave={(raw) => void saveEncoderConcurrency(encoder.id, isLocal, raw)}
                    />
                  </td>
                  <td>
                    {jobs.length > 0 ? (
                      <div className={styles["encoder-job-info"]}>
                        {jobs.map((j) => (
                          <div key={j.packageId}>
                            <span className={styles["encoder-job-title"]}>{j.mediaTitle || j.mediaId}</span>
                            <span className="muted encoder-job-profile"> — {j.profile}</span>
                          </div>
                        ))}
                      </div>
                    ) : (
                      <span className="muted">idle</span>
                    )}
                  </td>
                  <td>
                    {job ? (
                      job.progressPct != null ? (
                        <span>{job.progressPct}%</span>
                      ) : (
                        <span className="muted">{formatMs(now - job.claimedAtMs)}</span>
                      )
                    ) : (
                      <span className="muted">—</span>
                    )}
                  </td>
                  <td>{details.host}</td>
                  <td>{details.system}</td>
                  <td className={styles["encoder-detail-cell"]} title={details.gpu}>{details.gpu}</td>
                  <td className={`${styles["encoder-disk-cell"]}${formatDiskFree(details.diskFreeGB).tone ? ` is-${formatDiskFree(details.diskFreeGB).tone}` : ""}`}>
                    {formatDiskFree(details.diskFreeGB).label}
                  </td>
                  <td className={styles["encoder-detail-cell"]}>{details.ip}</td>
                  <td>{formatTimestamp(encoder.lastSeenMs)}</td>
                  <td className={styles["encoder-actions"]}>
                    {isLocal ? (
                      <button
                        type="button"
                        onClick={() => void toggleLocalWorker((encoder as LocalWorkerItem).concurrency)}
                      >
                        {(encoder as LocalWorkerItem).enabled ? "Disable" : "Enable"}
                      </button>
                    ) : (
                      <EncoderActionsMenu
                        encoder={encoder}
                        busy={encoderBusy}
                        onRevoke={() => void revoke(encoder.id, encoder.name)}
                        onDelete={() => void deleteRow(encoder.id, encoder.name)}
                      />
                    )}
                  </td>
                </tr>
              );
            })}
            {visibleEncoders.length === 0 && (
              <tr>
                <td colSpan={12} className="muted">
                  {encoders.length === 0
                    ? "no encoders registered"
                    : "no active encoders — toggle below to show revoked"}
                </td>
              </tr>
            )}
          </tbody>
        </table>
        {revokedCount > 0 && (
          <div className={styles["encoder-revoked-toggle"]}>
            <button type="button" className="link-button" onClick={() => setShowRevoked((v) => !v)}>
              {showRevoked
                ? `hide ${revokedCount} revoked`
                : `show ${revokedCount} revoked`}
            </button>
          </div>
        )}
        {encoderStatus && <p className="channel-status-msg muted">{encoderStatus}</p>}
      </section>

      <section className="admin-panel-section encoding-status-section">
        <div className="section-headline">
          <h2>Encoding status</h2>
          <div className={styles["encoding-head-actions"]}>
            <button
              type="button"
              className="danger"
              disabled={cancelBusy || ((counts.pending ?? 0) + (counts.processing ?? 0)) === 0}
              onClick={() => void cancelQueuedAndEncoding()}
            >
              {cancelBusy ? "cancelling…" : "Cancel queued/encoding"}
            </button>
            <button type="button" disabled={loading} onClick={() => loadCandidates(false)}>
              {loading ? "refreshing" : "refresh"}
            </button>
          </div>
        </div>
        <div className={styles["encoding-status-grid"]}>
          <StatusMetric
            label="encoded"
            value={counts.ready ?? 0}
            active={statusFilter === "ready"}
            onClick={() => setStatusFilter(statusFilter === "ready" ? "" : "ready")}
            tone={(counts.ready ?? 0) > 0 ? "good" : undefined}
          />
          <StatusMetric
            label="missing"
            value={counts.missing ?? 0}
            active={statusFilter === "missing"}
            onClick={() => setStatusFilter(statusFilter === "missing" ? "" : "missing")}
          />
          <StatusMetric
            label="failed"
            value={counts.failed ?? 0}
            active={statusFilter === "failed"}
            onClick={() => setStatusFilter(statusFilter === "failed" ? "" : "failed")}
            tone={(counts.failed ?? 0) > 0 ? "bad" : undefined}
          />
          <StatusMetric
            label="queued"
            value={counts.pending ?? 0}
            active={statusFilter === "pending"}
            onClick={() => setStatusFilter(statusFilter === "pending" ? "" : "pending")}
          />
          <StatusMetric
            label="encoding"
            value={counts.processing ?? 0}
            active={statusFilter === "processing"}
            onClick={() => setStatusFilter(statusFilter === "processing" ? "" : "processing")}
            tone={(counts.processing ?? 0) > 0 ? "active" : undefined}
          />
        </div>
        {activeJobs.length > 0 && (
          <table className={styles["encoding-active-table"]}>
            <thead>
              <tr>
                <th>encoding</th>
                <th>duration</th>
                <th>elapsed</th>
              </tr>
            </thead>
            <tbody>
              {activeJobs.map((job) => {
                const elapsedMs = job.updatedAtMs != null ? now - job.updatedAtMs : null;
                const title = job.title || job.path.split("/").pop() || job.mediaId;
                return (
                  <tr key={job.mediaId}>
                    <td className={styles["encoding-active-title"]}>{title}</td>
                    <td className={styles["encoding-active-num"]}>{formatMs(job.durationMs)}</td>
                    <td className={styles["encoding-active-num"]}>{formatMs(elapsedMs)}</td>
                  </tr>
                );
              })}
            </tbody>
          </table>
        )}
        {lastResult && (
          <div className={styles["encoding-run-summary"]}>
            <span>queued {lastResult.queued.length}</span>
            <span>already queued {lastResult.alreadyPending.length}</span>
            <span>ready {lastResult.alreadyReady.length}</span>
            <span>failed {lastResult.failed.length}</span>
          </div>
        )}
        {status && <p className="channel-status-msg muted">{status}</p>}
      </section>

      <section className="admin-panel-section">
        <div className={styles["encoding-toolbar"]}>
          <label>
            <span>profile</span>
            <select
              value={profile}
              onChange={(e) => setProfile(e.target.value)}
            >
              <option value={ALL_PROFILES}>All profiles</option>
              {profiles.map((item) => (
                <option key={item} value={item}>{profileOptionLabel(item, profileDetails[item])}</option>
              ))}
            </select>
          </label>
          <label>
            <span>search</span>
            <input
              value={filter}
              placeholder="title, path, group, status…"
              onChange={(e) => setFilter(e.target.value)}
            />
          </label>
          <label>
            <span>status</span>
            <select value={statusFilter} onChange={(e) => setStatusFilter(e.target.value)}>
              <option value="">all non-ready</option>
              <option value="failed">failed</option>
              <option value="pending">queued</option>
              <option value="processing">encoding</option>
              <option value="missing">missing</option>
              <option value="ready">encoded</option>
            </select>
          </label>
          <button type="button" disabled={submitBusy || selectedCount === 0 || profile === ALL_PROFILES} onClick={() => void submitSelected()}>
            {submitBusy ? "Queueing…" : `Queue selected (${selectedCount})`}
          </button>
        </div>

        <div className={styles["encoding-select-row"]}>
          <label>
            <input
              type="checkbox"
              checked={allSelectableChecked}
              disabled={selectableRows.length === 0}
              onChange={(e) => toggleAllSelectable(e.target.checked)}
            />
            <span>select visible queueable</span>
          </label>
          <span className="muted">
            {data
              ? `${visibleRows.length}/${rows.length} loaded, ${data.count} ${countLabel} for ${data.profile === ALL_PROFILES ? "all profiles" : data.profile}`
              : "loading media"}
          </span>
        </div>

        <ul className={styles["encoding-media-list"]}>
          {visibleRows.map((media) => {
            const title = media.title || media.path.split("/").pop() || media.mediaId;
            const checked = selectedIds.has(media.mediaId);
            const statusLabel = packageStatusLabel(media.packageStatus, media.packageProfile || data?.profile || profile, profileDetails);
            return (
              <li key={media.mediaId} className={`${styles["encoding-media-row"]} status-${media.packageStatus}`}>
                <label className={styles["encoding-media-check"]}>
                  <input
                    type="checkbox"
                    checked={checked}
                    disabled={!media.selectable || submitBusy}
                    onChange={(e) => toggleMedia(media.mediaId, e.target.checked)}
                  />
                </label>
                <div className={styles["encoding-media-main"]}>
                  <span className={styles["encoding-media-title"]}>{title}</span>
                  <span className="muted encoding-media-path">{media.path}</span>
                  {media.packageError && <span className="danger encoding-media-error">{media.packageError}</span>}
                </div>
                <div className={styles["encoding-media-meta"]}>
                  <span
                    className={`episode-pkg ${packageStatusClass(media.packageStatus)}`}
                    title={statusLabel.title}
                  >
                    {statusLabel.text}
                  </span>
                  <span>{formatMs(media.durationMs)}</span>
                </div>
              </li>
            );
          })}
          {!loading && data && rows.length === 0 && !status && (
            <li className="encoding-empty muted">
              {data.profile === ALL_PROFILES
                ? "no non-ready package rows exist across profiles"
                : "all codec-passing media has a ready package for this profile"}
            </li>
          )}
          {!loading && data && rows.length > 0 && visibleRows.length === 0 && (
            <li className="encoding-empty muted">no media matches this search</li>
          )}
        </ul>
        {rows.length > 0 && rows.length % 100 === 0 && (
          <div className={styles["encoding-load-more"]}>
            <button type="button" disabled={loadingMore} onClick={loadMore}>
              {loadingMore ? "loading…" : `load more (${rows.length} loaded)`}
            </button>
          </div>
        )}
      </section>
    </div>
  );
}

function profileOptionLabel(name: string, detail?: PackageProfile) {
  return detail?.label ? `${detail.label} (${name})` : name;
}

function profileChipLabel(name: string, detail?: PackageProfile) {
  return detail?.label || name;
}

function packageStatusLabel(
  status: string,
  profile: string,
  details: Record<string, PackageProfile>,
): { text: string; title: string } {
  if (status === "ready") {
    const label = profileChipLabel(profile, details[profile]);
    return { text: label, title: `ready: ${profile}` };
  }
  return { text: status, title: status };
}

function StatusMetric({
  label,
  value,
  tone,
  active,
  onClick,
}: {
  label: string;
  value: number;
  tone?: "active" | "bad" | "good";
  active?: boolean;
  onClick?: () => void;
}) {
  return (
    <button
      type="button"
      className={`${styles["encoding-status-metric"]}${tone ? ` is-${tone}` : ""}${active ? " is-selected" : ""}`}
      onClick={onClick}
    >
      <span>{label}</span>
      <strong>{value}</strong>
    </button>
  );
}

function packageStatusClass(status: string): string {
  if (status === "ready") return "episode-pkg-ready";
  if (status === "failed") return "episode-pkg-failed";
  if (status === "missing") return "episode-pkg-missing";
  return "episode-pkg-pending";
}

// Encoders idle-poll /ping every 30s ([cmd/linearcast-encoder/main.go]
// defaultIdlePollInterval) and heartbeat more often while a job is running.
// A 90s stale threshold tolerates one missed poll before the UI demotes a row
// from "online" to "offline" — derived from last_seen_ms because the server
// never downgrades the persisted status column on its own.
const encoderStaleAfterMs = 90_000;

function isEncoderLive(encoder: EncoderListItem, nowMs: number): boolean {
  if (!encoder.lastSeenMs) return false;
  return nowMs - encoder.lastSeenMs < encoderStaleAfterMs;
}

function encoderBadgeLabel(encoder: EncoderListItem | LocalWorkerItem, nowMs: number): string {
  if (!isRemoteEncoder(encoder)) {
    if (!encoder.enabled) return "disabled";
    return (encoder.jobs?.length ?? 0) > 0 ? "online" : "idle";
  }
  if (encoder.revokedAtMs) return "revoked";
  if (encoder.status === "pending") return "awaiting first ping";
  return isEncoderLive(encoder, nowMs) ? "online" : "offline";
}

function encoderBadgeClass(encoder: EncoderListItem | LocalWorkerItem, nowMs: number): string {
  if (!isRemoteEncoder(encoder)) {
    if (!encoder.enabled) return "episode-pkg-failed";
    return (encoder.jobs?.length ?? 0) > 0 ? "episode-pkg-ready" : "episode-pkg-missing";
  }
  if (encoder.revokedAtMs) return "episode-pkg-failed";
  if (encoder.status === "pending") return "episode-pkg-missing";
  return isEncoderLive(encoder, nowMs) ? "episode-pkg-ready" : "episode-pkg-pending";
}

function formatTimestamp(ms?: number): string {
  if (!ms || ms <= 0) return "never";
  return new Date(ms).toLocaleString();
}

function encoderDetails(encoder: EncoderListItem | LocalWorkerItem): {
  host: string;
  system: string;
  gpu: string;
  ip: string;
  diskFreeGB: number | null;
} {
  if (!isRemoteEncoder(encoder)) {
    const caps = (encoder.capabilities ?? {}) as any;
    const reported = (caps.reported ?? {}) as any;
    const diskFreeGB = typeof reported.diskFreeGB === "number" ? reported.diskFreeGB : null;
    return { host: "local", system: "—", gpu: "—", ip: "—", diskFreeGB };
  }
  const caps = (encoder.capabilities ?? {}) as any;
  const reported = (caps.reported ?? {}) as any;
  const os = reported.os || "";
  const arch = reported.arch || "";
  const gpus = Array.isArray(reported.nvidiaGpus) ? reported.nvidiaGpus : [];
  const encoderNames = Array.isArray(reported.encoders) ? reported.encoders : [];
  const gpuNames = gpus
    .map((gpu: any) => [gpu?.name, gpu?.driverVersion ? `driver ${gpu.driverVersion}` : ""].filter(Boolean).join(" "))
    .filter(Boolean);
  const gpu = gpuNames.length > 0
    ? gpuNames.join(", ")
    : encoderNames.length > 0
      ? encoderNames.join(", ")
      : "—";
  const diskFreeGB = typeof reported.diskFreeGB === "number" ? reported.diskFreeGB : null;
  return {
    host: reported.hostname || "—",
    system: [os, arch].filter(Boolean).join(" ") || "—",
    gpu,
    ip: caps.lastRemoteAddr || "—",
    diskFreeGB,
  };
}

function formatDiskFree(gb: number | null): { label: string; tone: "warn" | "danger" | "" } {
  if (gb === null) return { label: "—", tone: "" };
  const label = gb < 10 ? `${gb.toFixed(1)} GB` : `${Math.round(gb)} GB`;
  const tone = gb < 10 ? "danger" : gb < 50 ? "warn" : "";
  return { label, tone };
}

function EncoderActionsMenu({
  encoder,
  busy,
  onRevoke,
  onDelete,
}: {
  encoder: EncoderListItem;
  busy: boolean;
  onRevoke: () => void;
  onDelete: () => void;
}) {
  const [open, setOpen] = useState(false);
  const ref = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    function onClick(event: MouseEvent) {
      if (ref.current && !ref.current.contains(event.target as Node)) {
        setOpen(false);
      }
    }
    document.addEventListener("mousedown", onClick);
    return () => document.removeEventListener("mousedown", onClick);
  }, [open]);

  return (
    <div className="encoder-actions-menu" ref={ref}>
      <button
        type="button"
        className={styles["encoder-actions-toggle"]}
        disabled={busy}
        onClick={() => setOpen((v) => !v)}
        aria-label="actions"
        aria-expanded={open}
      >
        ⋮
      </button>
      {open && (
        <div className={styles["encoder-actions-dropdown"]}>
          {!encoder.revokedAtMs && (
            <button type="button" className="danger" disabled={busy} onClick={() => { setOpen(false); onRevoke(); }}>
              Revoke key
            </button>
          )}
          <button type="button" className="danger" disabled={busy} onClick={() => { setOpen(false); onDelete(); }}>
            Delete
          </button>
        </div>
      )}
    </div>
  );
}

function EncoderDownloadControl({
  downloads,
  downloadsError,
  selectedPlatform,
  onSelect,
}: {
  downloads: EncoderDownloadsResponse | null;
  downloadsError: string;
  selectedPlatform: EncoderPlatform;
  onSelect: (platform: EncoderPlatform) => void;
}) {
  const selectedEntry = findDownload(downloads?.available ?? [], selectedPlatform);
  let title = "Download encoder binary";
  if (downloadsError) title = downloadsError;
  else if (!downloads) title = "Checking available encoder builds";
  else if (!downloads.distConfigured) title = "Encoder downloads are not configured on this server";
  else if (!selectedEntry) title = `${platformLabel(selectedPlatform)} is not built on this server`;

  return (
    <div className={styles["encoder-download-control"]}>
      <label>
        <span>download</span>
        <select
          value={selectedPlatform}
          onChange={(event) => onSelect(event.target.value as EncoderPlatform)}
        >
          {PRIMARY_ENCODER_DOWNLOADS.map((opt) => (
            <option key={opt.platform} value={opt.platform}>{opt.label}</option>
          ))}
        </select>
      </label>
      {selectedEntry ? (
        <a
          className={`${styles["encoder-download-button"]} is-recommended`}
          href={encoderDownloadURL(selectedEntry.platform)}
          download={selectedEntry.filename}
          title={title}
        >
          Download
        </a>
      ) : (
        <button type="button" disabled title={title}>
          Download
        </button>
      )}
    </div>
  );
}

function EncoderRegisteredDialog({
  encoder,
  downloads,
  downloadsError,
  onClose,
}: {
  encoder: EncoderRegisterResponse | null;
  downloads: EncoderDownloadsResponse | null;
  downloadsError: string;
  onClose: () => void;
}) {
  const open = encoder !== null;
  const adminUrl = open ? `${window.location.protocol}//${window.location.host}` : "";
  const detectedOS = detectOS();
  const [selectedOS, setSelectedOS] = useState<EncoderPlatform>(detectedOS);

  // Reset the OS selector to the freshly detected default each time the dialog
  // (re)opens, so a previous session's pick doesn't stick around.
  useEffect(() => {
    if (open) setSelectedOS(detectedOS);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [open]);

  const available = downloads?.available ?? [];
  const selectedEntry = findDownload(available, selectedOS);

  return (
    <Dialog open={open} onClose={onClose} title="Register remote encoder">
      {encoder && (
        <div className={styles["encoder-setup-dialog"]}>
          <p className="muted">
            <strong>{encoder.name}</strong> is registered. This API key is shown <strong>once</strong> — save it before
            closing.
          </p>
          <div className={styles["encoder-token-panel"]}>
            <div className={styles["encoder-token-row"]}>
              <code>{encoder.apiKey}</code>
            </div>
          </div>

          <div className={styles["encoder-setup-os-tabs"]}>
            {ENCODER_PLATFORM_OPTIONS.map((opt) => (
              <button
                key={opt.platform}
                type="button"
                className={selectedOS === opt.platform ? "is-active" : ""}
                onClick={() => setSelectedOS(opt.platform)}
              >
                {opt.label}
                {opt.platform === detectedOS && <span className="muted"> · this machine</span>}
              </button>
            ))}
          </div>

          {downloadsError && <p className="plex-token-error">{downloadsError}</p>}
          {!downloads && !downloadsError && <p className="muted">loading available builds…</p>}
          {downloads && !downloads.distConfigured && (
            <p className="muted">
              The server has no encoder dist directory configured. Set <code>LINEARCAST_ENCODER_DIST_DIR</code> on the
              linearcast-admin process, or rebuild the Docker image to populate <code>/opt/linearcast/encoder-dist</code>.
            </p>
          )}
          {downloads && downloads.distConfigured && available.length === 0 && (
            <p className="muted">No encoder binaries found on the server. Rebuild and redeploy to populate them.</p>
          )}
          {downloads && selectedEntry && (
            <a
              className={`${styles["encoder-download-button"]} is-recommended`}
              href={encoderDownloadURL(selectedEntry.platform)}
              download={selectedEntry.filename}
            >
              Download {selectedEntry.filename}
            </a>
          )}
          {downloads && available.length > 0 && !selectedEntry && (
            <p className="muted">
              The server does not have a binary for {platformLabel(selectedOS)} on disk. Rebuild and redeploy to add it,
              or pick a different platform above.
            </p>
          )}

          <EncoderSetupSections plan={renderSetupPlan(selectedOS, encoder.apiKey, adminUrl)} />
          <p className="muted">
            Requires <code>ffmpeg</code> and <code>ffprobe</code> via <code>LINEARCAST_FFMPEG_DIR</code>, a bundled{" "}
            <code>tools</code> / <code>ffmpeg/bin</code> folder beside the encoder binary, or <code>PATH</code>. The
            work directory is used for scratch downloads and tarball staging.
          </p>
        </div>
      )}
    </Dialog>
  );
}

function EncoderSetupSections({ plan }: { plan: SetupPlan }) {
  return (
    <div className={styles["encoder-setup-sections"]}>
      {plan.unitFile && (
        <>
          <h4>1. Download the service file</h4>
          <button
            type="button"
            className={styles["encoder-download-button"]}
            onClick={() => downloadBlob(plan.unitFile!.filename, plan.unitFile!.mimeType, plan.unitFile!.body)}
          >
            Download {plan.unitFile.filename}
          </button>
          <details className={styles["encoder-setup-details"]}>
            <summary>view file contents</summary>
            <pre className={styles["encoder-setup-snippet"]}>{plan.unitFile.body}</pre>
          </details>
        </>
      )}
      <h4>{plan.unitFile ? "2. Install and start" : "Setup instructions"}</h4>
      <pre className={styles["encoder-setup-snippet"]}>{plan.install}</pre>
      {plan.manage && (
        <details className={styles["encoder-setup-details"]}>
          <summary>status, logs, uninstall</summary>
          <pre className={styles["encoder-setup-snippet"]}>{plan.manage}</pre>
        </details>
      )}
    </div>
  );
}

type SetupPlan = {
  // Optional unit/plist file the operator installs into a service manager.
  // When set, the dialog offers a download button and shows the file body.
  unitFile?: { filename: string; mimeType: string; body: string };
  // The shell snippet the operator runs to install and start the encoder.
  install: string;
  // Optional follow-up commands (status/logs/uninstall) shown collapsed.
  manage?: string;
};

function renderSetupPlan(platform: string, apiKey: string, adminUrl: string): SetupPlan {
  if (platform === "windows-amd64") {
    const binary = `linearcast-encoder-windows-amd64.exe`;
    const install = [
      `:: Run from the folder where you saved the .exe (e.g. %USERPROFILE%\\linearcast-encoder).`,
      `set LINEARCAST_ADMIN_URL=${adminUrl}`,
      `set LINEARCAST_ENCODER_API_KEY=${apiKey}`,
      `set LINEARCAST_ENCODER_WORK_DIR=%USERPROFILE%\\linearcast-encoder-work`,
      `${binary} check`,
      `${binary} install`,
    ].join("\n");
    const manage = [
      `:: Start now (also runs at next logon)`,
      `"%APPDATA%\\Microsoft\\Windows\\Start Menu\\Programs\\Startup\\linearcast-encoder.bat"`,
      ``,
      `:: Stop`,
      `taskkill /IM linearcast-encoder-windows-amd64.exe /F`,
      ``,
      `:: Uninstall (removes the Startup script)`,
      `${binary} uninstall`,
    ].join("\n");
    return { install, manage };
  }
  const binary = `linearcast-encoder-${platform}`;
  if (platform.startsWith("darwin")) {
    return {
      unitFile: {
        filename: "com.linearcast.encoder.plist",
        mimeType: "application/xml",
        body: launchdPlist({ binary, apiKey, adminUrl, ffmpegDir: defaultMacFFmpegDir(platform) }),
      },
      install: macInstallScript(binary),
      manage: macManageScript(),
    };
  }
  // linux-amd64 / linux-arm64
  return {
    unitFile: {
      filename: "linearcast-encoder.service",
      mimeType: "text/plain",
      body: systemdUnit({ binary, apiKey, adminUrl }),
    },
    install: linuxInstallScript(binary),
    manage: linuxManageScript(),
  };
}

function systemdUnit({ binary, apiKey, adminUrl }: { binary: string; apiKey: string; adminUrl: string }): string {
  // %h is the systemd specifier for the user's home directory, expanded at
  // runtime by systemd itself — so the same unit file works for any user.
  return [
    `[Unit]`,
    `Description=Linearcast remote encoder`,
    `After=network-online.target`,
    `Wants=network-online.target`,
    ``,
    `[Service]`,
    `Type=simple`,
    `Environment=LINEARCAST_ADMIN_URL=${adminUrl}`,
    `Environment=LINEARCAST_ENCODER_API_KEY=${apiKey}`,
    `Environment=LINEARCAST_ENCODER_WORK_DIR=%h/.linearcast-encoder/work`,
    `ExecStart=%h/.linearcast-encoder/${binary} run`,
    `Restart=on-failure`,
    `RestartSec=10`,
    ``,
    `[Install]`,
    `WantedBy=default.target`,
    ``,
  ].join("\n");
}

function launchdPlist({
  binary,
  apiKey,
  adminUrl,
  ffmpegDir,
}: {
  binary: string;
  apiKey: string;
  adminUrl: string;
  ffmpegDir: string;
}): string {
  // launchd doesn't expand ~ or $HOME inside the plist, so we use the literal
  // placeholder __HOME__ and have the install snippet substitute it for $HOME
  // at install time.
  return [
    `<?xml version="1.0" encoding="UTF-8"?>`,
    `<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">`,
    `<plist version="1.0">`,
    `<dict>`,
    `  <key>Label</key><string>com.linearcast.encoder</string>`,
    `  <key>ProgramArguments</key>`,
    `  <array>`,
    `    <string>__HOME__/.linearcast-encoder/${binary}</string>`,
    `    <string>run</string>`,
    `  </array>`,
    `  <key>EnvironmentVariables</key>`,
    `  <dict>`,
    `    <key>LINEARCAST_ADMIN_URL</key><string>${adminUrl}</string>`,
    `    <key>LINEARCAST_ENCODER_API_KEY</key><string>${apiKey}</string>`,
    `    <key>LINEARCAST_ENCODER_WORK_DIR</key><string>__HOME__/.linearcast-encoder/work</string>`,
    `    <key>LINEARCAST_FFMPEG_DIR</key><string>${ffmpegDir}</string>`,
    `  </dict>`,
    `  <key>KeepAlive</key><true/>`,
    `  <key>RunAtLoad</key><true/>`,
    `  <key>ThrottleInterval</key><integer>30</integer>`,
    `  <key>StandardOutPath</key><string>__HOME__/.linearcast-encoder/encoder.log</string>`,
    `  <key>StandardErrorPath</key><string>__HOME__/.linearcast-encoder/encoder.log</string>`,
    `</dict>`,
    `</plist>`,
    ``,
  ].join("\n");
}

function defaultMacFFmpegDir(platform: string): string {
  return platform === "darwin-amd64" ? "/usr/local/bin" : "/opt/homebrew/bin";
}

function macInstallScript(binary: string): string {
  return [
    `mkdir -p ~/.linearcast-encoder/work`,
    `mv ~/Downloads/${binary} ~/.linearcast-encoder/${binary}`,
    `chmod +x ~/.linearcast-encoder/${binary}`,
    `xattr -d com.apple.quarantine ~/.linearcast-encoder/${binary} 2>/dev/null || true`,
    ``,
    `mv ~/Downloads/com.linearcast.encoder.plist ~/Library/LaunchAgents/`,
    `sed -i '' "s|__HOME__|$HOME|g" ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
    `launchctl load ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
  ].join("\n");
}

function macManageScript(): string {
  return [
    `# Status`,
    `launchctl list | grep linearcast`,
    ``,
    `# Logs`,
    `tail -f ~/.linearcast-encoder/encoder.log`,
    ``,
    `# Stop / uninstall`,
    `launchctl unload ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
    `rm ~/Library/LaunchAgents/com.linearcast.encoder.plist`,
    `rm -rf ~/.linearcast-encoder`,
  ].join("\n");
}

function linuxInstallScript(binary: string): string {
  return [
    `mkdir -p ~/.linearcast-encoder/work`,
    `mv ~/Downloads/${binary} ~/.linearcast-encoder/${binary}`,
    `chmod +x ~/.linearcast-encoder/${binary}`,
    ``,
    `mkdir -p ~/.config/systemd/user`,
    `mv ~/Downloads/linearcast-encoder.service ~/.config/systemd/user/`,
    `systemctl --user daemon-reload`,
    `systemctl --user enable --now linearcast-encoder`,
    ``,
    `# Optional: keep running after you log out`,
    `loginctl enable-linger $USER`,
  ].join("\n");
}

function linuxManageScript(): string {
  return [
    `# Status`,
    `systemctl --user status linearcast-encoder`,
    ``,
    `# Logs`,
    `journalctl --user -u linearcast-encoder -f`,
    ``,
    `# Stop / uninstall`,
    `systemctl --user disable --now linearcast-encoder`,
    `rm ~/.config/systemd/user/linearcast-encoder.service`,
    `rm -rf ~/.linearcast-encoder`,
  ].join("\n");
}

function downloadBlob(filename: string, mimeType: string, body: string) {
  const blob = new Blob([body], { type: mimeType });
  const url = URL.createObjectURL(blob);
  const a = document.createElement("a");
  a.href = url;
  a.download = filename;
  document.body.appendChild(a);
  a.click();
  document.body.removeChild(a);
  // Revoke after a short delay so the click has time to settle in Firefox.
  setTimeout(() => URL.revokeObjectURL(url), 0);
}

function findDownload(entries: EncoderDownloadEntry[], platform: EncoderPlatform): EncoderDownloadEntry | null {
  return entries.find((entry) => entry.platform === platform) ?? null;
}

function defaultPrimaryPlatform(): EncoderPlatform {
  const platform = detectOS();
  if (platform === "windows-amd64") return "windows-amd64";
  if (platform === "darwin-arm64" || platform === "darwin-amd64") return "darwin-arm64";
  return "linux-amd64";
}

function detectOS(): EncoderPlatform {
  const platform = detectPlatform();
  if (platform === "darwin-arm64" || platform === "darwin-amd64") return platform;
  if (platform === "windows-amd64") return platform;
  if (platform === "linux-arm64" || platform === "linux-amd64") return platform;
  return "linux-amd64";
}

function platformLabel(platform: string): string {
  switch (platform) {
    case "darwin-arm64": return "macOS (Apple Silicon)";
    case "darwin-amd64": return "macOS (Intel)";
    case "windows-amd64": return "Windows";
    case "linux-amd64": return "Linux (x86_64)";
    case "linux-arm64": return "Linux (ARM64)";
    default: return platform;
  }
}

function detectPlatform(): string {
  if (typeof navigator === "undefined") return "";
  const ua = navigator.userAgent.toLowerCase();
  const platform = (navigator.platform || "").toLowerCase();
  if (ua.includes("windows") || platform.includes("win")) return "windows-amd64";
  if (ua.includes("mac") || platform.includes("mac")) {
    // Apple Silicon is the common case on modern Macs. Browsers don't reliably
    // expose arch, so we default to arm64 and let the user pick Intel if needed.
    return "darwin-arm64";
  }
  if (ua.includes("linux") || platform.includes("linux")) {
    if (ua.includes("aarch64") || ua.includes("arm64")) return "linux-arm64";
    return "linux-amd64";
  }
  return "";
}

function candidateCountLabel(status: string): string {
  switch (status) {
    case "ready":
      return "total encoded";
    case "failed":
      return "total failed";
    case "pending":
      return "total queued";
    case "processing":
      return "total encoding";
    case "missing":
      return "total missing";
    default:
      return "total non-ready";
  }
}
