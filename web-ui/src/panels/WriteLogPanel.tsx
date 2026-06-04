import { useCallback, useState } from "react";
import { getAdminWriteLog } from "../api";
import { usePolling } from "../hooks/usePolling";
import type { AdminWriteLogEntry } from "../types";
import styles from "./WriteLogPanel.module.css";

const REFRESH_MS = 5000;
const LIMIT_OPTIONS = [50, 100, 250, 500];

export function WriteLogPanel() {
  const [entries, setEntries] = useState<AdminWriteLogEntry[]>([]);
  const [limit, setLimit] = useState(100);
  const [error, setError] = useState("");
  const [loading, setLoading] = useState(false);
  const [updatedAt, setUpdatedAt] = useState<number | null>(null);

  const refresh = useCallback(async (signal?: AbortSignal) => {
    setLoading(true);
    try {
      const body = await getAdminWriteLog(limit, signal);
      setEntries(body.entries ?? []);
      setError("");
      setUpdatedAt(Date.now());
    } catch (err) {
      if (signal?.aborted) return;
      setError(err instanceof Error ? err.message : String(err));
      throw err;
    } finally {
      if (!signal?.aborted) setLoading(false);
    }
  }, [limit]);

  usePolling({
    intervalMs: REFRESH_MS,
    maxIntervalMs: 60_000,
    resetKey: limit,
    task: refresh,
  });

  const stamp = updatedAt ? new Date(updatedAt).toLocaleTimeString() : "—";

  return (
    <div className="admin-panel">
      <section className="admin-panel-section">
        <div className="section-headline">
          <h2>Operator write log</h2>
          <div className={styles["write-log-controls"]}>
            <label>
              <span className="muted">limit</span>{" "}
              <select
                value={limit}
                onChange={(e) => setLimit(Number(e.target.value))}
              >
                {LIMIT_OPTIONS.map((n) => (
                  <option key={n} value={n}>
                    {n}
                  </option>
                ))}
              </select>
            </label>
            <button
              type="button"
              disabled={loading}
              onClick={() => void refresh()}
            >
              {loading ? "refreshing" : "refresh"}
            </button>
            <span className="muted">updated {stamp}</span>
          </div>
        </div>

        {error && <p className="danger">{error}</p>}

        {entries.length === 0 ? (
          <p className="muted">
            {error ? "no entries available" : "no write actions recorded yet"}
          </p>
        ) : (
          <div className={styles["write-log-table-wrap"]}>
            <table className={styles["write-log-table"]}>
              <thead>
                <tr>
                  <th>time</th>
                  <th>action</th>
                  <th>target</th>
                  <th>method</th>
                  <th>path</th>
                  <th>status</th>
                  <th>duration</th>
                </tr>
              </thead>
              <tbody>
                {entries.map((row) => {
                  const failed = row.status >= 400;
                  return (
                    <tr key={row.id} className={failed ? "is-failed" : ""}>
                      <td title={new Date(row.createdAtMs).toISOString()}>
                        {new Date(row.createdAtMs).toLocaleTimeString()}
                      </td>
                      <td>{row.action ?? <span className="muted">—</span>}</td>
                      <td>
                        {row.targetType || row.targetId ? (
                          <>
                            {row.targetType && (
                              <span className="muted">{row.targetType}:</span>
                            )}{" "}
                            {row.targetId ?? ""}
                          </>
                        ) : (
                          <span className="muted">—</span>
                        )}
                      </td>
                      <td>{row.method}</td>
                      <td className={styles["write-log-path"]} title={row.path}>
                        {row.path}
                      </td>
                      <td className={failed ? "danger" : ""}>{row.status}</td>
                      <td>{row.durationMs}ms</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </div>
  );
}
