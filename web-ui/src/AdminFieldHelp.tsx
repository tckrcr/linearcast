export function AdminFieldLabel({ label, help }: { label: string; help: string }) {
  return (
    <span className="admin-field-label">
      <span>{label}</span>
      <span
        className="admin-field-help"
        tabIndex={0}
        role="img"
        aria-label={`${label}: ${help}`}
        title={help}
        data-help={help}
      >
        i
      </span>
    </span>
  );
}
