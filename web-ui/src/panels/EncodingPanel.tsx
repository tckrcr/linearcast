import { FormEvent, useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import {
  cancelMediaPackages,
  deleteMediaPackages,
  deleteEncoder,
  encoderDownloadURL,
  getEncoderDownloads,
  getEncoders,
  getMediaPackageCandidates,
  getMediaPackageProfileList,
  registerEncoder,
  requestMediaPackages,
  revokeEncoder,
  stopChannelEncoder,
  updateEncoderConcurrency,
  updateLocalWorker,
} from "../api";
import { Dialog } from "../Dialog";
import {
  ENCODER_PLATFORM_OPTIONS,
  PRIMARY_ENCODER_DOWNLOADS,
  defaultPrimaryPlatform,
  detectOS,
  findDownload,
  platformLabel,
  renderSetupPlan,
} from "../encoderSetup";
import type { EncoderPlatform, SetupPlan } from "../encoderSetup";
import { formatBytes, formatMs } from "../format";
import { usePolling } from "../hooks/usePolling";
import { StatusBadge } from "../ui/StatusBadge";
import type { StatusTone } from "../ui/StatusBadge";
import type {
  EncoderDownloadsResponse,
  EncoderListItem,
  EncoderRegisterResponse,
  LocalWorkerItem,
  MediaPackageCandidateList,
  MediaPackageRequestResult,
  OnDemandEncodingItem,
  PackageProfile,
  SizeEstimate,
} from "../types";
import styles from "./EncodingPanel.module.css";

const ALL_PROFILES = "all";

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
  const [reclaimingId, setReclaimingId] = useState<string | null>(null);
  const [status, setStatus] = useState("");
  const [lastResult, setLastResult] = useState<MediaPackageRequestResult | null>(null);
  const [now, setNow] = useState(() => Date.now());
  const [encoders, setEncoders] = useState<Array<EncoderListItem | LocalWorkerItem>>([]);
  const [onDemandEncodings, setOnDemandEncodings] = useState<OnDemandEncodingItem[]>([]);
  const [killingEncoding, setKillingEncoding] = useState<string | null>(null);
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
        const visible = next.profiles;
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
      setOnDemandEncodings(next.onDemandEncodings ?? []);
      if (!silent) setEncoderStatus("");
    } catch (err) {
      if (signal?.aborted) return;
      if (!silent) setEncoderStatus(err instanceof Error ? err.message : String(err));
      throw err;
    }
  }, []);

  const handleKillEncoding = useCallback(async (channelId: string) => {
    setKillingEncoding(channelId);
    try {
      await stopChannelEncoder(channelId);
      void loadEncoders(false);
    } catch (err) {
      setEncoderStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setKillingEncoding(null);
    }
  }, [loadEncoders]);

  useEffect(() => { void loadEncoders(false); }, [loadEncoders]);
  usePolling({
    intervalMs: 10_000,
    maxIntervalMs: 60_000,
    task: (signal) => loadEncoders(true, signal),
  });

  useEffect(() => {
    const es = new EventSource("/api/admin/encoder-events");
    es.onmessage = (e: MessageEvent<string>) => {
      try {
        const ev = JSON.parse(e.data) as { packageId: string; progressPct?: number; leaseExpiresMs: number };
        setEncoders((prev) =>
          prev.map((enc) => {
            if (!enc.jobs?.some((j) => j.packageId === ev.packageId)) return enc;
            return {
              ...enc,
              jobs: enc.jobs.map((j) =>
                j.packageId === ev.packageId
                  ? { ...j, progressPct: ev.progressPct, leaseExpiresMs: ev.leaseExpiresMs }
                  : j,
              ),
            };
          }),
        );
      } catch {
        // malformed event — ignore
      }
    };
    return () => es.close();
  }, []);

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
  const estimateNote = sizeEstimateNote(profile, data);
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

  async function reclaimPackages(mediaId: string, title: string) {
    if (reclaimingId || profile === ALL_PROFILES) return;
    const scope = profile.trim();
    if (!window.confirm(`Delete "${title}" packages for profile "${scope}"? Encoded files will be removed from disk.`)) return;
    setReclaimingId(mediaId);
    setStatus(`reclaiming packages for "${title}"…`);
    try {
      const result = await deleteMediaPackages(mediaId, scope);
      const freed = formatBytes(result.totalBytes);
      if (result.skippedRows > 0) {
        setStatus(`reclaimed ${result.deletedRows} package${result.deletedRows === 1 ? "" : "s"} (${freed}); ${result.skippedRows} skipped — in use by a channel`);
      } else {
        setStatus(`reclaimed ${result.deletedRows} package${result.deletedRows === 1 ? "" : "s"} (${freed})`);
      }
      loadCandidates(true);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setReclaimingId(null);
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
          <div className="section-headline-main">
            <h2>Encoders</h2>
            <p className="section-purpose">
              The local encoder plus any registered remote workers that package media.
            </p>
          </div>
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
        <div className={styles["encoder-table-wrap"]}>
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
                    <StatusBadge tone={encoderBadgeTone(encoder, now)}>
                      {encoderBadgeLabel(encoder, now)}
                    </StatusBadge>
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
        </div>
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
        {onDemandEncodings.length > 0 && (
          <div className={styles["encoder-table-wrap"]}>
            <table className={styles["encoder-table"]}>
              <thead>
                <tr>
                  <th>channel encoding</th>
                  <th>status</th>
                  <th>channel</th>
                  <th>media</th>
                  <th>profile</th>
                  <th>segments</th>
                  <th>elapsed</th>
                  <th>last progress</th>
                  <th></th>
                </tr>
              </thead>
              <tbody>
                {onDemandEncodings.map((encoding) => {
                  const title = encoding.mediaTitle || encoding.mediaId;
                  const channel = encoding.channelName || encoding.channelId;
                  const killing = killingEncoding === encoding.channelId;
                  return (
                    <tr key={encoding.encodingId}>
                      <td>
                        <span className={styles["encoder-name"]}>On-demand encoding</span>
                        <span className={`muted ${styles["encoder-id"]}`}>{encoding.encodingId}</span>
                      </td>
                      <td>
                        <StatusBadge tone={encoding.processRunning ? "good" : "neutral"}>
                          {encoding.state}
                        </StatusBadge>
                      </td>
                      <td>{channel}</td>
                      <td className={styles["encoder-detail-cell"]} title={title}>{title}</td>
                      <td>{encoding.profile}</td>
                      <td>{encoding.segmentCount}</td>
                      <td>{formatMs(now - encoding.spawnedAtMs)}</td>
                      <td>{formatMs(now - encoding.lastProgressMs)}</td>
                      <td>
                        <button
                          className="btn btn-danger btn-sm"
                          onClick={() => void handleKillEncoding(encoding.channelId)}
                          disabled={killing}
                          title="Kill encoder (channel gated 15 min)"
                        >
                          {killing ? "…" : "✕"}
                        </button>
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="admin-panel-section encoding-status-section">
        <div className="section-headline">
          <div className="section-headline-main">
            <h2>Encoding status</h2>
            <p className="section-purpose">
              Queue health and per-job progress. Cancel pending work or retry failures here.
            </p>
          </div>
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
        {activeJobs.length > 0 && (() => {
          const progressByMediaId = new Map<string, number>();
          for (const enc of encoders) {
            for (const j of enc.jobs ?? []) {
              if (j.progressPct != null) progressByMediaId.set(j.mediaId, j.progressPct);
            }
          }
          return (
            <table className={styles["encoding-active-table"]}>
              <thead>
                <tr>
                  <th>encoding</th>
                  <th>duration</th>
                  <th>elapsed</th>
                  <th>progress</th>
                </tr>
              </thead>
              <tbody>
                {activeJobs.map((job) => {
                  const elapsedMs = job.updatedAtMs != null ? now - job.updatedAtMs : null;
                  const title = job.title || job.path.split("/").pop() || job.mediaId;
                  const pct = progressByMediaId.get(job.mediaId);
                  return (
                    <tr key={job.mediaId}>
                      <td className={styles["encoding-active-title"]}>{title}</td>
                      <td className={styles["encoding-active-num"]}>{formatMs(job.durationMs)}</td>
                      <td className={styles["encoding-active-num"]}>{formatMs(elapsedMs)}</td>
                      <td className={styles["encoding-active-num"]}>
                        {pct != null ? `${pct}%` : <span className="muted">—</span>}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          );
        })()}
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
              placeholder="title, group, status..."
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
          {estimateNote && <span className="muted">· {estimateNote}</span>}
        </div>

        <ul className={styles["encoding-media-list"]}>
          {visibleRows.map((media) => {
            const title = media.title || media.path.split("/").pop() || media.mediaId;
            const checked = selectedIds.has(media.mediaId);
            const statusLabel = packageStatusLabel(media.packageStatus, media.packageProfile || data?.profile || profile, profileDetails);
            const sizeLabel = packageSizeLabel(media);
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
                  <span className={styles["encoding-media-path"]} title={media.path}>{media.path}</span>
                  {media.packageError && <span className="danger encoding-media-error">{media.packageError}</span>}
                </div>
                <div className={styles["encoding-media-meta"]}>
                  <StatusBadge tone={packageStatusTone(media.packageStatus)} title={statusLabel.title}>
                    {statusLabel.text}
                  </StatusBadge>
                  <span>{formatMs(media.durationMs)}</span>
                  {sizeLabel && (
                    <span className="muted" title={sizeLabel.title}>{sizeLabel.text}</span>
                  )}
                  {(media.packageStatus === "ready" || media.packageStatus === "failed") && (
                    <button
                      type="button"
                      className="danger"
                      disabled={profile === ALL_PROFILES || reclaimingId != null}
                      title={profile === ALL_PROFILES ? "select a specific profile to reclaim packages" : "delete encoded packages from disk"}
                      onClick={() => void reclaimPackages(media.mediaId, title)}
                    >
                      {reclaimingId === media.mediaId ? "reclaiming…" : "reclaim"}
                    </button>
                  )}
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

// sizeEstimateLabel renders a media's estimated package size. Copy/target/CBR
// profiles give a firm "≈ N" expected size; CRF profiles (no empirical bitrate
// yet) can only bound it, shown as "≤ N" from the profile's max-bitrate ceiling.
// Returns null when there is nothing to show (no estimate, or an uncapped CRF
// profile with no known size).
function sizeEstimateLabel(est: SizeEstimate | undefined): { text: string; title: string } | null {
  if (!est) return null;
  if (est.expectedKnown && est.expectedBytes > 0) {
    let title: string;
    switch (est.mode) {
      case "copy":
        title = "expected size (copy: video remuxed at source bitrate)";
        break;
      case "target":
      case "cbr":
        title = "expected size (target bitrate)";
        break;
      default:
        // crf / capped-crf: measured from this profile's finished packages.
        title = est.maxBytes > 0
          ? `estimated from this profile's encoded packages (ceiling ≤ ${formatBytes(est.maxBytes)})`
          : "estimated from this profile's encoded packages";
    }
    return { text: `≈ ${formatBytes(est.expectedBytes)}`, title };
  }
  if (est.maxBytes > 0) {
    return {
      text: `≤ ${formatBytes(est.maxBytes)}`,
      title: "worst-case ceiling (CRF output size depends on content; capped at the profile's max bitrate)",
    };
  }
  return null;
}

function packageSizeLabel(media: { packageStatus: string; packageBytes?: number; sizeEstimate?: SizeEstimate }): { text: string; title: string } | null {
  if (media.packageStatus === "ready" && media.packageBytes != null) {
    return {
      text: formatBytes(media.packageBytes),
      title: "actual encoded package size",
    };
  }
  return sizeEstimateLabel(media.sizeEstimate);
}

// sizeEstimateNote summarizes the basis of the size column for the selected
// profile, so a missing expected size is explained rather than silent. CRF
// profiles say how many finished packages back the estimate (or that there are
// none yet, hence ceiling-only).
function sizeEstimateNote(profile: string, data: MediaPackageCandidateList | null): string | null {
  if (!data || profile === ALL_PROFILES) return null;
  const mode = data.media.find((m) => m.sizeEstimate)?.sizeEstimate?.mode;
  if (!mode) return null;
  if (mode === "copy") return "size ≈ exact (copy: source bitrate)";
  if (mode === "target" || mode === "cbr") return "size ≈ from target bitrate";
  // crf / capped-crf
  const n = data.estimateSamples ?? 0;
  return n > 0
    ? `size ≈ measured from ${n} finished package${n === 1 ? "" : "s"}`
    : "size ≤ ceiling only — no finished packages yet to estimate from";
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

function packageStatusTone(status: string): StatusTone {
  if (status === "ready") return "good";
  if (status === "failed") return "danger";
  if (status === "missing") return "neutral";
  return "warn";
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

function encoderBadgeTone(encoder: EncoderListItem | LocalWorkerItem, nowMs: number): StatusTone {
  if (!isRemoteEncoder(encoder)) {
    if (!encoder.enabled) return "danger";
    return (encoder.jobs?.length ?? 0) > 0 ? "good" : "neutral";
  }
  if (encoder.revokedAtMs) return "danger";
  if (encoder.status === "pending") return "neutral";
  return isEncoderLive(encoder, nowMs) ? "good" : "warn";
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
  // The dropdown is rendered in a portal with fixed positioning so it escapes
  // the encoder table's horizontal-scroll container, which would otherwise clip
  // it and grow a stray vertical scrollbar. Coords are right-anchored to the
  // toggle button.
  const [coords, setCoords] = useState<{ top: number; right: number } | null>(null);
  const toggleRef = useRef<HTMLButtonElement>(null);
  const dropdownRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    if (!open) return;
    const button = toggleRef.current;
    if (!button) return;
    const reposition = () => {
      const rect = button.getBoundingClientRect();
      setCoords({ top: rect.bottom + 4, right: window.innerWidth - rect.right });
    };
    reposition();
    function onClick(event: MouseEvent) {
      const target = event.target as Node;
      if (toggleRef.current?.contains(target)) return;
      if (dropdownRef.current?.contains(target)) return;
      setOpen(false);
    }
    document.addEventListener("mousedown", onClick);
    window.addEventListener("resize", reposition);
    // Capture scroll on any ancestor (e.g. the table wrap) so the menu tracks.
    window.addEventListener("scroll", reposition, true);
    return () => {
      document.removeEventListener("mousedown", onClick);
      window.removeEventListener("resize", reposition);
      window.removeEventListener("scroll", reposition, true);
    };
  }, [open]);

  return (
    <div className={styles["encoder-actions-menu"]}>
      <button
        ref={toggleRef}
        type="button"
        className={styles["encoder-actions-toggle"]}
        disabled={busy}
        onClick={() => setOpen((v) => !v)}
        aria-label="actions"
        aria-expanded={open}
      >
        ⋮
      </button>
      {open && coords &&
        createPortal(
          <div
            ref={dropdownRef}
            className={styles["encoder-actions-dropdown"]}
            style={{ top: coords.top, right: coords.right }}
          >
            {!encoder.revokedAtMs && (
              <button type="button" className="danger" disabled={busy} onClick={() => { setOpen(false); onRevoke(); }}>
                Revoke key
              </button>
            )}
            <button type="button" className="danger" disabled={busy} onClick={() => { setOpen(false); onDelete(); }}>
              Delete
            </button>
          </div>,
          document.body,
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
  // (re)opens, so a previous encoding's pick doesn't stick around.
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
              linearcast process, or rebuild the Docker image to populate <code>/opt/linearcast/encoder-dist</code>.
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
