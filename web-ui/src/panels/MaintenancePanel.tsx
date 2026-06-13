import { useState } from "react";
import {
  cleanupMissingMedia,
  cleanupOrphanPackages,
  getPackageIntegrity,
  optimizeDatabase,
  reclaimMediaEncodes,
} from "../api";
import { formatBytes } from "../format";
import type {
  EncodeReclaimResponse,
  MissingMediaMaintenanceResponse,
  OptimizeDBMaintenanceResponse,
  OrphanPackagesMaintenanceResponse,
  PackageIntegrityItem,
  PackageIntegrityResponse,
} from "../types";
import styles from "./MaintenancePanel.module.css";
import toolsStyles from "./ToolsPanel.module.css";

type BusyAction = "" | "missing" | "orphans" | "optimize" | "integrity" | "encodes";

// describeIntegrityProblem renders the reason a checked package is not OK, in
// priority order: structural file errors, then missing segments, then duration.
function describeIntegrityProblem(item: PackageIntegrityItem): string {
  if (item.fileError) return item.fileError;
  if (!item.initPresent) return "init segment missing";
  const missing = item.missingSegments?.length ?? 0;
  if (missing > 0) return `${missing} segment(s) missing`;
  if (item.truncated) return "packaged duration short of source";
  if (item.durationUnknown) return "duration unknown";
  return "not ok";
}

export function MaintenancePanel({ onChanged }: { onChanged: () => void }) {
  const [busyAction, setBusyAction] = useState<BusyAction>("");
  const [status, setStatus] = useState("");
  const [missingResult, setMissingResult] = useState<MissingMediaMaintenanceResponse | null>(null);
  const [orphanResult, setOrphanResult] = useState<OrphanPackagesMaintenanceResponse | null>(null);
  const [optimizeResult, setOptimizeResult] = useState<OptimizeDBMaintenanceResponse | null>(null);
  const [integrityResult, setIntegrityResult] = useState<PackageIntegrityResponse | null>(null);
  const [reclaimResult, setReclaimResult] = useState<EncodeReclaimResponse | null>(null);
  const [encodeMediaId, setEncodeMediaId] = useState("");
  const [encodeForce, setEncodeForce] = useState(false);

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

  async function checkPackageIntegrity() {
    setBusyAction("integrity");
    setStatus("checking package integrity...");
    try {
      const result = await getPackageIntegrity();
      setIntegrityResult(result);
      setStatus(
        `checked ${result.checked} ready package(s): ${result.problems} problem(s), ${result.unknownDuration} unknown duration`,
      );
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyAction("");
    }
  }

  async function deleteMediaEncodes() {
    const media = encodeMediaId.trim();
    if (!media) {
      setStatus("enter a media id to delete its encodes");
      return;
    }
    setBusyAction("encodes");
    setStatus("scanning encodes...");
    try {
      const preview = await reclaimMediaEncodes(media, { force: encodeForce, dryRun: true });
      setReclaimResult(preview);
      if (preview.candidates === 0) {
        setStatus(`no packages found for media ${media}`);
        return;
      }
      const deletable = preview.candidates - preview.skippedRows;
      if (deletable === 0) {
        setStatus(
          `all ${preview.candidates} package(s) for ${media} are still referenced by a channel; enable force to delete anyway`,
        );
        return;
      }
      const skipNote = preview.skippedRows
        ? ` ${preview.skippedRows} referenced package(s) will be skipped.`
        : "";
      const msg = `Delete ${deletable} encode package(s) for media ${media} (${formatBytes(preview.totalBytes)})?${skipNote}`;
      if (!window.confirm(msg)) {
        setStatus("encode deletion cancelled");
        return;
      }
      const result = await reclaimMediaEncodes(media, { force: encodeForce, dryRun: false });
      setReclaimResult(result);
      setStatus(
        `deleted ${result.deletedRows} package(s) (${formatBytes(result.totalBytes)}); ${result.skippedRows} skipped`,
      );
      onChanged();
    } catch (err) {
      setStatus(err instanceof Error ? err.message : String(err));
    } finally {
      setBusyAction("");
    }
  }

  const disabled = busyAction !== "";
  const integrityProblems = integrityResult?.packages.filter((p) => p.checked && !p.ok) ?? [];

  return (
    <section className="admin-panel-section">
      <div className="section-headline">
        <h2>Maintenance</h2>
      </div>
      <div className={styles["maintenance-actions"]}>
        <button type="button" disabled={disabled} onClick={() => void cleanMissingMedia()}>
          {busyAction === "missing" ? "Scanning..." : "Clean missing media"}
        </button>
        <button type="button" disabled={disabled} onClick={() => void cleanOrphanPackages()}>
          {busyAction === "orphans" ? "Scanning..." : "Clean orphan packages"}
        </button>
        <button type="button" disabled={disabled} onClick={() => void checkPackageIntegrity()}>
          {busyAction === "integrity" ? "Checking..." : "Check package integrity"}
        </button>
        <button type="button" disabled={disabled} onClick={() => void optimizeDB()}>
          {busyAction === "optimize" ? "Optimizing..." : "Optimize database"}
        </button>
      </div>

      <div className={styles["maintenance-encodes"]}>
        <input
          type="text"
          placeholder="media id"
          value={encodeMediaId}
          disabled={disabled}
          onChange={(e) => setEncodeMediaId(e.target.value)}
        />
        <label className={styles["maintenance-force"]}>
          <input
            type="checkbox"
            checked={encodeForce}
            disabled={disabled}
            onChange={(e) => setEncodeForce(e.target.checked)}
          />
          force (delete even if still referenced)
        </label>
        <button type="button" disabled={disabled || !encodeMediaId.trim()} onClick={() => void deleteMediaEncodes()}>
          {busyAction === "encodes" ? "Working..." : "Delete media encodes"}
        </button>
      </div>

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
        {integrityResult && (
          <span>
            package integrity: {integrityResult.checked} checked, {integrityResult.problems} problem(s), {integrityResult.unknownDuration} unknown duration
          </span>
        )}
        {reclaimResult && (
          <span>
            encodes: {reclaimResult.deletedRows} deleted, {reclaimResult.skippedRows} skipped, {formatBytes(reclaimResult.totalBytes)}
          </span>
        )}
        {optimizeResult && (
          <span>
            database: {formatBytes(optimizeResult.sizeBefore)} to {formatBytes(optimizeResult.sizeAfter)}
          </span>
        )}
        {status && <span className={`muted ${toolsStyles["cache-warning"]}`}>{status}</span>}
        {integrityProblems.map((item) => (
          <span key={item.packageId} className={`muted ${toolsStyles["cache-warning"]}`}>
            {item.profile} {item.mediaId}: {describeIntegrityProblem(item)}
          </span>
        ))}
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
        {reclaimResult?.warnings?.map((warning) => (
          <span key={warning} className={`muted ${toolsStyles["cache-warning"]}`}>
            {warning}
          </span>
        ))}
      </div>
    </section>
  );
}
