import { useState } from "react";
import {
  backfillMediaOrdering,
  checkSchedule,
  cleanupMissingMedia,
  cleanupOrphanPackages,
  getPackageIntegrity,
  importPackages,
  optimizeDatabase,
  repairPackageIntegrity,
  requeuePackage,
} from "../api";
import { formatBytes } from "../format";
import type {
  ImportPackagesResponse,
  MissingMediaMaintenanceResponse,
  OptimizeDBMaintenanceResponse,
  OrphanPackagesMaintenanceResponse,
  PackageIntegrityItem,
  PackageIntegrityResponse,
  ScheduleCheckIssue,
  ScheduleCheckResponse,
} from "../types";
import styles from "./MaintenancePanel.module.css";
import toolsStyles from "./ToolsPanel.module.css";

type BusyAction = "" | "missing" | "orphans" | "optimize";

function describeIntegrityProblem(item: PackageIntegrityItem): string {
  if (item.fileError) return item.fileError;
  if (!item.initPresent) return "init segment missing";
  const missing = item.missingSegments?.length ?? 0;
  if (missing > 0) return `${missing} segment(s) missing`;
  if (item.truncated) return "packaged duration short of source";
  if (item.durationUnknown) return "duration unknown";
  return "not ok";
}

function issueKindLabel(kind: string): string {
  switch (kind) {
    case "gap": return "gap";
    case "overlap": return "overlap";
    case "invalid_alignment": return "misaligned";
    case "missing_media": return "missing media";
    case "media_bounds": return "out of bounds";
    case "package_not_ready": return "not packaged";
    case "no_schedule": return "empty";
    default: return kind;
  }
}

// ── Schedule check ───────────────────────────────────────────────────────────

function ScheduleCheckSection({ disabled }: { disabled: boolean }) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ScheduleCheckResponse | null>(null);
  const [status, setStatus] = useState("");
  const [showAll, setShowAll] = useState(false);

  async function run() {
    setBusy(true);
    setStatus("checking schedules…");
    setResult(null);
    try {
      const res = await checkSchedule();
      setResult(res);
      setStatus(
        res.issues.length === 0
          ? `${res.channelsChecked} channel(s) checked — no issues`
          : `${res.channelsChecked} channel(s) checked — ${res.issues.length} issue(s)`
      );
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  const visibleIssues = showAll ? (result?.issues ?? []) : (result?.issues.slice(0, 20) ?? []);

  return (
    <div>
      <button type="button" disabled={disabled || busy} onClick={() => void run()}>
        {busy ? "Checking…" : "Check schedules"}
      </button>
      {status && <span className={`muted ${toolsStyles["cache-warning"]}`}>{status}</span>}
      {result && result.issues.length > 0 && (
        <div className={styles["maintenance-summary"]} style={{ marginTop: 6 }}>
          {visibleIssues.map((iss, i) => (
            <IssueRow key={i} issue={iss} />
          ))}
          {!showAll && result.issues.length > 20 && (
            <button type="button" className={styles["media-edit-btn"]} onClick={() => setShowAll(true)}>
              show all {result.issues.length} issues
            </button>
          )}
        </div>
      )}
    </div>
  );
}

function IssueRow({ issue }: { issue: ScheduleCheckIssue }) {
  return (
    <span className={`muted ${toolsStyles["cache-warning"]}`}>
      [{issueKindLabel(issue.kind)}] {issue.channelId}
      {issue.mediaId ? ` · ${issue.mediaId}` : ""}
      {" — "}{issue.message}
    </span>
  );
}

// ── Package integrity (check + repair) ───────────────────────────────────────

// PackageIntegritySection merges the read-only integrity check with the repair
// sweep. "Check" is the safe entry point; once it surfaces problems, a
// contextual "Repair all" button runs the same sweep the encoder sweeper runs
// on its timer (requeuing every broken ready package), and each problem also
// gets an inline per-package "requeue" for granular control. Both repair paths
// re-check afterward so the list reflects what is left.
function PackageIntegritySection({ disabled }: { disabled: boolean }) {
  const [busy, setBusy] = useState<"" | "check" | "repair">("");
  const [result, setResult] = useState<PackageIntegrityResponse | null>(null);
  const [status, setStatus] = useState("");

  async function check() {
    setBusy("check");
    setStatus("checking package integrity…");
    try {
      const res = await getPackageIntegrity();
      setResult(res);
      setStatus(
        `checked ${res.checked} ready package(s): ${res.problems} problem(s), ${res.unknownDuration} unknown duration`,
      );
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  async function repairAll() {
    setBusy("repair");
    setStatus("running integrity repair sweep…");
    try {
      const repaired = await repairPackageIntegrity();
      const requeued = repaired.fileReset + repaired.durationReset;
      const fresh = await getPackageIntegrity();
      setResult(fresh);
      setStatus(
        requeued === 0
          ? `sweep complete — nothing requeued; ${fresh.problems} problem(s) remain`
          : `requeued ${repaired.fileReset} file + ${repaired.durationReset} duration error(s); ${fresh.problems} problem(s) remain`,
      );
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy("");
    }
  }

  const problems = result?.packages.filter((p) => p.checked && !p.ok) ?? [];

  return (
    <div>
      <button type="button" disabled={disabled || busy !== ""} onClick={() => void check()}>
        {busy === "check" ? "Checking…" : "Check package integrity"}
      </button>
      {result && result.problems > 0 && (
        <button
          type="button"
          disabled={disabled || busy !== ""}
          onClick={() => void repairAll()}
          title="requeue every broken ready package for re-encoding"
        >
          {busy === "repair" ? "Repairing…" : `Repair all ${result.problems}`}
        </button>
      )}
      {status && <span className={`muted ${toolsStyles["cache-warning"]}`}>{status}</span>}
      {problems.length > 0 && (
        <div className={styles["maintenance-summary"]} style={{ marginTop: 6 }}>
          {problems.map((item) => (
            <span key={item.packageId} className={`muted ${toolsStyles["cache-warning"]}`}>
              <RequeueButton packageId={item.packageId} onRequeued={() => void check()} />
              {" "}
              {item.profile} {item.mediaId}: {describeIntegrityProblem(item)}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

// ── Backfill episode ordering ────────────────────────────────────────────────

function BackfillOrderingSection({ disabled }: { disabled: boolean }) {
  const [busy, setBusy] = useState(false);
  const [status, setStatus] = useState("");

  async function run() {
    setBusy(true);
    setStatus("backfilling media ordering…");
    try {
      const res = await backfillMediaOrdering();
      setStatus(`scanned ${res.scanned} row(s), updated ${res.updated}`);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <button type="button" disabled={disabled || busy} onClick={() => void run()}>
        {busy ? "Backfilling…" : "Backfill episode ordering"}
      </button>
      {status && <span className={`muted ${toolsStyles["cache-warning"]}`}>{status}</span>}
    </div>
  );
}

// ── Import packages from disk ────────────────────────────────────────────────

// ImportPackagesSection rebuilds DB rows for finalized packages already on disk
// without re-encoding — the disaster-recovery counterpart to packaging. It
// attaches every package whose media row exists (rescan source folders first to
// restore those), and reports any whose media is still missing.
function ImportPackagesSection({
  disabled,
  onChanged,
}: {
  disabled: boolean;
  onChanged: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [result, setResult] = useState<ImportPackagesResponse | null>(null);
  const [status, setStatus] = useState("");

  async function run() {
    setBusy(true);
    setStatus("scanning package cache…");
    setResult(null);
    try {
      const res = await importPackages();
      setResult(res);
      const parts = [
        `scanned ${res.scanned} package(s): imported ${res.imported.length}`,
        `${res.alreadyReady} already ready`,
      ];
      if (res.needsMedia.length > 0) parts.push(`${res.needsMedia.length} need a media row (rescan first)`);
      if (res.skipped.length > 0) parts.push(`${res.skipped.length} skipped`);
      setStatus(parts.join(", "));
      if (res.imported.length > 0) onChanged();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div>
      <button
        type="button"
        disabled={disabled || busy}
        onClick={() => void run()}
        title="rebuild DB rows for finalized packages already on disk, without re-encoding"
      >
        {busy ? "Importing…" : "Import packages from disk"}
      </button>
      {status && <span className={`muted ${toolsStyles["cache-warning"]}`}>{status}</span>}
      {result && (result.needsMedia.length > 0 || result.skipped.length > 0) && (
        <div className={styles["maintenance-summary"]} style={{ marginTop: 6 }}>
          {result.needsMedia.slice(0, 20).map((mediaId) => (
            <span key={mediaId} className={`muted ${toolsStyles["cache-warning"]}`}>
              {mediaId}: no media row — rescan source folders first
            </span>
          ))}
          {result.skipped.slice(0, 20).map((item) => (
            <span key={item.path} className={`muted ${toolsStyles["cache-warning"]}`}>
              {item.path}: {item.reason}
            </span>
          ))}
        </div>
      )}
    </div>
  );
}

// ── Per-package requeue button ───────────────────────────────────────────────

function RequeueButton({
  packageId,
  onRequeued,
}: {
  packageId: string;
  onRequeued: () => void;
}) {
  const [busy, setBusy] = useState(false);
  const [done, setDone] = useState(false);

  async function requeue() {
    setBusy(true);
    try {
      const res = await requeuePackage(packageId);
      if (res.requeued) {
        setDone(true);
        onRequeued();
      }
    } catch {
      // silently ignore — parent's integrity check can be re-run
    } finally {
      setBusy(false);
    }
  }

  if (done) return <span className="muted" style={{ fontSize: 10 }}>queued</span>;
  return (
    <button
      type="button"
      className={styles["media-edit-btn"]}
      disabled={busy}
      onClick={() => void requeue()}
      title="requeue this package for re-encoding"
    >
      {busy ? "…" : "requeue"}
    </button>
  );
}

// ── Main panel ───────────────────────────────────────────────────────────────

export function MaintenancePanel({ onChanged }: { onChanged: () => void }) {
  const [busyAction, setBusyAction] = useState<BusyAction>("");
  const [status, setStatus] = useState("");
  const [missingResult, setMissingResult] = useState<MissingMediaMaintenanceResponse | null>(null);
  const [orphanResult, setOrphanResult] = useState<OrphanPackagesMaintenanceResponse | null>(null);
  const [optimizeResult, setOptimizeResult] = useState<OptimizeDBMaintenanceResponse | null>(null);

  async function cleanMissingMedia() {
    setBusyAction("missing");
    setStatus("scanning missing media...");
    try {
      const preview = await cleanupMissingMedia(true);
      setMissingResult(preview);
      if (preview.missing.length === 0) {
        setStatus(`checked ${preview.checked} media rows; no missing files`);
        return;
      }
      const msg = `Delete ${preview.missing.length} missing media row(s)? This also removes dependent schedule, history, and package rows.`;
      if (!window.confirm(msg)) {
        setStatus("missing media cleanup cancelled");
        return;
      }
      const result = await cleanupMissingMedia(false);
      setMissingResult(result);
      setStatus(`deleted ${result.deleted} missing media row(s)`);
      onChanged();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyAction("");
    }
  }

  async function cleanOrphanPackages() {
    setBusyAction("orphans");
    setStatus("scanning orphan packages...");
    try {
      const preview = await cleanupOrphanPackages(true);
      setOrphanResult(preview);
      const count = preview.unreferenced.length + preview.orphanDirs.length;
      if (count === 0) {
        setStatus("no orphan packages found");
        return;
      }
      const msg = `Delete ${preview.unreferenced.length} unreferenced package row(s) and ${preview.orphanDirs.length} orphan package director${preview.orphanDirs.length === 1 ? "y" : "ies"} (${formatBytes(preview.totalBytes)})?`;
      if (!window.confirm(msg)) {
        setStatus("orphan package cleanup cancelled");
        return;
      }
      const result = await cleanupOrphanPackages(false);
      setOrphanResult(result);
      setStatus(
        `deleted ${result.deletedRows} package row(s) and ${result.deletedDirs} orphan director${result.deletedDirs === 1 ? "y" : "ies"}`,
      );
      onChanged();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyAction("");
    }
  }

  async function optimizeDB() {
    const msg = "Optimize database now? This blocks writes until VACUUM finishes.";
    if (!window.confirm(msg)) return;
    setBusyAction("optimize");
    setStatus("optimizing database...");
    try {
      const result = await optimizeDatabase();
      setOptimizeResult(result);
      setStatus(`optimized in ${result.durationMs} ms`);
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyAction("");
    }
  }

  const disabled = busyAction !== "";

  return (
    <>
      <section className="admin-panel-section">
        <h2>Maintenance</h2>
        <p className="section-purpose">
          One-off cleanup and repair jobs for the library and package cache.
        </p>

        <div className={styles["maintenance-actions"]}>
          <button type="button" disabled={disabled} onClick={() => void cleanMissingMedia()}>
            {busyAction === "missing" ? "Scanning..." : "Clean missing media"}
          </button>
          <button type="button" disabled={disabled} onClick={() => void cleanOrphanPackages()}>
            {busyAction === "orphans" ? "Scanning..." : "Clean orphan packages"}
          </button>
          <button type="button" disabled={disabled} onClick={() => void optimizeDB()}>
            {busyAction === "optimize" ? "Optimizing..." : "Optimize database"}
          </button>
          <PackageIntegritySection disabled={disabled} />
          <ImportPackagesSection disabled={disabled} onChanged={onChanged} />
          <ScheduleCheckSection disabled={disabled} />
          <BackfillOrderingSection disabled={disabled} />
        </div>

        {/* ── Result summaries ── */}
        <div className={styles["maintenance-summary"]}>
          {missingResult && (
            <span>
              missing media: {missingResult.missing.length} found / {missingResult.deleted} deleted
            </span>
          )}
          {orphanResult && (
            <span>
              orphan packages: {orphanResult.unreferenced.length} rows, {orphanResult.orphanDirs.length} dirs, {formatBytes(orphanResult.totalBytes)}
            </span>
          )}
          {optimizeResult && (
            <span>
              database: {formatBytes(optimizeResult.sizeBefore)} to {formatBytes(optimizeResult.sizeAfter)}
            </span>
          )}
          {status && <span className={`muted ${toolsStyles["cache-warning"]}`}>{status}</span>}

          {missingResult?.errors?.map((item) => (
            <span key={`${item.id}:${item.path}`} className={`muted ${toolsStyles["cache-warning"]}`}>
              {item.path}: {item.error}
            </span>
          ))}
          {orphanResult?.warnings?.map((warning) => (
            <span key={warning} className={`muted ${toolsStyles["cache-warning"]}`}>
              {warning}
            </span>
          ))}
        </div>
      </section>

    </>
  );
}
