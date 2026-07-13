import { ReactNode, useEffect, useRef } from "react";
import styles from "./Dialog.module.css";

type Props = {
  open: boolean;
  onClose: () => void;
  title: string;
  children: ReactNode;
};

export function Dialog({ open, onClose, title, children }: Props) {
  const ref = useRef<HTMLDialogElement>(null);

  useEffect(() => {
    const el = ref.current;
    if (!el) return;
    if (open && !el.open) el.showModal();
    if (!open && el.open) el.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      className={styles.dialog}
      onCancel={(event) => {
        event.preventDefault();
        onClose();
      }}
    >
      <div className={styles["dialog-header"]}>
        <h3>{title}</h3>
        <button type="button" className="link-button" onClick={onClose} aria-label="Close">
          ×
        </button>
      </div>
      <div className={styles["dialog-body"]}>{children}</div>
    </dialog>
  );
}
