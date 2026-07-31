// A pill showing one piece of state: an encoder's liveness, a package's
// encoding outcome, whether a process is running.
//
// Tones are named for how they read, not for any single domain's vocabulary.
// The same four colours already serve all three of the cases above, so a
// meaning-based name ("ready", "failed") would be wrong at two of the three
// call sites — an online encoder is not "ready" and a running process is not
// "encoded". Callers map their own domain onto a tone.
import { ReactNode } from "react";
import styles from "./StatusBadge.module.css";

export type StatusTone = "good" | "warn" | "danger" | "neutral";

type Props = {
  tone?: StatusTone;
  // Hover text for detail that does not fit in the pill.
  title?: string;
  children: ReactNode;
};

export function StatusBadge({ tone = "neutral", title, children }: Props) {
  return (
    <span className={`${styles.badge} ${styles[tone]}`} title={title}>
      {children}
    </span>
  );
}
