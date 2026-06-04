import { useState } from "react";
import { cleanupMissingMedia, cleanupOrphanPackages, optimizeDatabase } from "../api";
import { formatBytes } from "../format";
import type {
  MissingMediaMaintenanceResponse,
  OptimizeDBMaintenanceResponse,
  OrphanPackagesMaintenanceResponse,
} from "../types";
import styles from "./MaintenancePanel.module.css";
import toolsStyles from "./ToolsPanel.module.css";

export function MaintenancePanel({ onChanged }: { onChanged: () => void }) {
  const [busyAction, setBusyAction] = useState<"" | "missing" | "orphans" | "optimize">("");
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
        <button type="button" disabled={disabled} onClick={() => void optimizeDB()}>
          {busyAction === "optimize" ? "Optimizing..." : "Optimize database"}
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
  );
}
