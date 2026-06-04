import { FormEvent, useEffect, useRef, useState } from "react";
import { Dialog } from "../Dialog";
import { createExternalChannel, describeProbeResult, probeUpstreamHLS } from "../api";
import styles from "./AddLiveChannelDialog.module.css";

type Props = {
  open: boolean;
  onClose: () => void;
  // Called once the channel has been created and the success confirmation has
  // been shown. The parent should refresh and return to the guide — the new
  // channel is not in the /api/now grid until linearcast's next refresh tick.
  onCreated: () => void;
};

// How long the "channel added" confirmation stays up before we return the user
// to the guide.
const SUCCESS_DISMISS_MS = 1800;

// AddLiveChannelDialog creates a live channel that proxies an upstream HLS
// manifest. Any reachable http(s) .m3u8 works — the server proxies whatever the
// URL serves. There is no limit on how many live channels can exist.
export function AddLiveChannelDialog({ open, onClose, onCreated }: Props) {
  const [displayName, setDisplayName] = useState("");
  const [upstreamHlsUrl, setUpstreamHlsUrl] = useState("");
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [probing, setProbing] = useState(false);
  const [probe, setProbe] = useState<{ ok: boolean; text: string } | null>(null);
  const [created, setCreated] = useState(false);
  const onCreatedRef = useRef(onCreated);
  onCreatedRef.current = onCreated;

  useEffect(() => {
    if (!open) return;
    setDisplayName("");
    setUpstreamHlsUrl("");
    setError("");
    setBusy(false);
    setProbing(false);
    setProbe(null);
    setCreated(false);
  }, [open]);

  // After a successful create, hold the confirmation briefly, then hand control
  // back to the parent (which refreshes and returns to the guide). The callback
  // is read from a ref so parent re-renders don't reset the timer.
  useEffect(() => {
    if (!created) return;
    const timer = window.setTimeout(() => onCreatedRef.current(), SUCCESS_DISMISS_MS);
    return () => window.clearTimeout(timer);
  }, [created]);

  async function testUrl() {
    const trimmed = upstreamHlsUrl.trim();
    if (!trimmed) return;
    setProbing(true);
    setProbe(null);
    try {
      setProbe(describeProbeResult(await probeUpstreamHLS(trimmed)));
    } catch (err) {
      setProbe({ ok: false, text: err instanceof Error ? err.message : String(err) });
    } finally {
      setProbing(false);
    }
  }

  async function submit(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setBusy(true);
    setError("");
    try {
      await createExternalChannel({
        displayName: displayName.trim(),
        upstreamHlsUrl: upstreamHlsUrl.trim(),
      });
      setCreated(true);
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      setBusy(false);
    }
  }

  if (created) {
    return (
      <Dialog open={open} onClose={onCreated} title="Channel added">
        <div className={styles.success}>
          <p className={styles.successTitle}>✓ Channel added</p>
          <p className="muted">
            It will appear in the guide momentarily — linearcast picks up new
            channels on its next ~60s refresh.
          </p>
        </div>
      </Dialog>
    );
  }

  return (
    <Dialog open={open} onClose={onClose} title="Add live channel">
      <form className={styles.form} onSubmit={(event) => void submit(event)}>
        <label>
          <span>Name</span>
          <input
            value={displayName}
            disabled={busy}
            placeholder="Live channel"
            onChange={(e) => setDisplayName(e.target.value)}
            required
          />
        </label>
        <label>
          <span>Upstream HLS URL</span>
          <input
            type="url"
            value={upstreamHlsUrl}
            disabled={busy}
            placeholder="https://example.com/stream.m3u8"
            onChange={(e) => {
              setUpstreamHlsUrl(e.target.value);
              setProbe(null);
            }}
            required
          />
        </label>
        <div className={styles.testRow}>
          <button
            type="button"
            onClick={() => void testUrl()}
            disabled={busy || probing || !upstreamHlsUrl.trim()}
          >
            {probing ? "Testing…" : "Test reachability"}
          </button>
          {probe && (
            <p className={probe.ok ? styles.probeOk : styles.probeWarn}>
              {probe.ok ? "✓ " : "⚠ "}
              {probe.text}
            </p>
          )}
        </div>
        <p className={`muted ${styles.hint}`}>
          The channel goes live immediately and proxies this manifest. linearcast
          picks it up on its next ~60s refresh.
        </p>
        {error && <p className={styles.error}>{error}</p>}
        <div className={styles.actions}>
          <button type="button" className="link-button" disabled={busy} onClick={onClose}>
            Cancel
          </button>
          <button type="submit" className="primary" disabled={busy}>
            {busy ? "Creating…" : "Create channel"}
          </button>
        </div>
      </form>
    </Dialog>
  );
}
