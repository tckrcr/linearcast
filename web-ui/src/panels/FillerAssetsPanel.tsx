import { useEffect, useState } from "react";
import { apiFetch } from "../api/client";
import type { FillerAsset } from "../types";
import styles from "./FillerAssetsPanel.module.css";

type FillerAssetsResponse = {
  count: number;
  assets: FillerAsset[];
};

export function FillerAssetsPanel() {
  const [assets, setAssets] = useState<FillerAsset[]>([]);
  const [loaded, setLoaded] = useState(false);
  const [error, setError] = useState("");

  useEffect(() => {
    let cancelled = false;
    apiFetch<FillerAssetsResponse>("/api/filler-assets", { cache: "no-store" })
      .then((r) => {
        if (!cancelled) setAssets(r.assets);
      })
      .catch((err) => {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      })
      .finally(() => {
        if (!cancelled) setLoaded(true);
      });
    return () => { cancelled = true; };
  }, []);

  return (
    <div className="admin-panel">
      <section className="admin-panel-section">
        <h2>Filler Library</h2>
        <p className="muted">
          Filler assets are video clips scheduled to fill gaps in slot-grid channels.
          Import them by adding a <strong>Local source</strong> with media kind <strong>filler</strong>{" "}
          under <strong>Tools → Media Sources</strong>, then scan it.
          Ready assets appear in the <strong>Filler</strong> tab of the schedule builder and can be dragged
          into gap slots.
        </p>

        {error && <div className="plex-token-error">{error}</div>}
        {!loaded && !error && <p className="muted">loading…</p>}

        {loaded && assets.length === 0 && !error && (
          <p className="muted">
            No filler assets yet. Create a local source with media kind &quot;filler&quot; and scan it.
          </p>
        )}

        {assets.length > 0 && (
          <ul className={styles["fa-list"]}>
            {assets.map((a) => (
              <li key={a.id} className={styles["fa-row"]}>
                <span className={styles["fa-label"]}>{a.label}</span>
                <span className={`muted ${styles["fa-kind"]}`}>{a.kind}</span>
                <span className={`muted ${styles["fa-status"]} ${a.enabled ? "" : styles["fa-disabled"]}`}>
                  {a.enabled ? "enabled" : "disabled"}
                </span>
              </li>
            ))}
          </ul>
        )}
      </section>
    </div>
  );
}
