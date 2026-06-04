import { ReactNode } from "react";
import { formatMs } from "../format";
import mpStyles from "./MediaPickerRail.module.css";

export type PickerRailItem = {
  key: string;
  title: string;
  meta?: ReactNode;
  durationMs?: number;
  disabled?: boolean;
  actionLabel?: string;
};

export type PickerRailTab = { id: string; label: string };

export type MediaPickerRailProps = {
  title?: ReactNode;
  subtitle?: ReactNode;
  onClose?: () => void;
  tabs?: PickerRailTab[];
  activeTab?: string;
  onTabChange?: (tab: string) => void;
  query: string;
  onQueryChange: (q: string) => void;
  queryPlaceholder?: string;
  searchDisabled?: boolean;
  onRefresh?: () => void;
  refreshing?: boolean;
  refreshLabel?: string;
  loading?: boolean;
  loadingMessage?: string;
  error?: string;
  notice?: ReactNode;
  toolsExtra?: ReactNode;
  items: PickerRailItem[];
  onItemAction: (key: string) => void;
  itemActionBusy?: boolean;
  emptyMessage?: string;
  footer?: ReactNode;
  defaultActionLabel?: string;
  // When set, the rail renders this slot below the tab bar and skips its own
  // filter input / list rendering. Used by tabs that want a custom body
  // (e.g. a poster grid).
  content?: ReactNode;
};

export function MediaPickerRail(props: MediaPickerRailProps) {
  const {
    title,
    subtitle,
    onClose,
    tabs,
    activeTab,
    onTabChange,
    query,
    onQueryChange,
    queryPlaceholder,
    searchDisabled,
    onRefresh,
    refreshing,
    refreshLabel = "Refresh",
    loading,
    loadingMessage = "loading…",
    error,
    notice,
    toolsExtra,
    items,
    onItemAction,
    itemActionBusy,
    emptyMessage,
    footer,
    defaultActionLabel = "Add",
    content,
  } = props;

  const showHeader = title != null || subtitle != null || onClose != null;
  const showTabs = tabs && tabs.length > 1;
  const showList = !loading && !error;
  const useCustomContent = content != null;

  return (
    <div className={mpStyles["mp-rail"]}>
      {showHeader && (
        <div className={mpStyles["mp-rail-head"]}>
          <div className={mpStyles["mp-rail-head-text"]}>
            {title != null && <strong>{title}</strong>}
            {subtitle != null && <span className="muted">{subtitle}</span>}
          </div>
          {onClose && (
            <button type="button" onClick={onClose} disabled={itemActionBusy}>
              Close
            </button>
          )}
        </div>
      )}
      {showTabs && (
        <div className={mpStyles["mp-rail-tabs"]} role="tablist">
          {tabs!.map((t) => (
            <button
              key={t.id}
              type="button"
              role="tab"
              aria-selected={activeTab === t.id}
              className={`${mpStyles["mp-rail-tab"]}${activeTab === t.id ? " is-active" : ""}`}
              onClick={() => onTabChange?.(t.id)}
            >
              {t.label}
            </button>
          ))}
        </div>
      )}
      {useCustomContent && content}
      {!useCustomContent && (
        <div className={mpStyles["mp-rail-tools"]}>
          <input
            className={mpStyles["mp-rail-input"]}
            value={query}
            placeholder={queryPlaceholder}
            disabled={searchDisabled}
            onChange={(e) => onQueryChange(e.target.value)}
          />
          {onRefresh && (
            <button type="button" disabled={refreshing} onClick={onRefresh}>
              {refreshing ? "…" : refreshLabel}
            </button>
          )}
          {toolsExtra}
        </div>
      )}
      {!useCustomContent && loading && <p className={`muted ${mpStyles["mp-rail-status"]}`}>{loadingMessage}</p>}
      {!useCustomContent && !loading && error && <p className={`error ${mpStyles["mp-rail-status"]}`}>{error}</p>}
      {!useCustomContent && !loading && !error && notice && <p className={`muted ${mpStyles["mp-rail-status"]}`}>{notice}</p>}
      {!useCustomContent && showList && items.length > 0 && (
        <ul className={mpStyles["mp-rail-list"]}>
          {items.map((item) => (
            <li key={item.key} className={mpStyles["mp-rail-row"]}>
              <div className={mpStyles["mp-rail-row-main"]}>
                <span className={mpStyles["mp-rail-row-title"]}>{item.title}</span>
                {item.meta != null && (
                  <span className={`muted ${mpStyles["mp-rail-row-meta"]}`}>{item.meta}</span>
                )}
              </div>
              {item.durationMs != null && (
                <span className={`muted ${mpStyles["mp-rail-row-dur"]}`}>
                  {formatMs(item.durationMs)}
                </span>
              )}
              <button
                type="button"
                className={`primary ${mpStyles["mp-rail-row-add"]}`}
                disabled={item.disabled || itemActionBusy}
                onClick={() => onItemAction(item.key)}
              >
                {item.actionLabel ?? defaultActionLabel}
              </button>
            </li>
          ))}
        </ul>
      )}
      {!useCustomContent && showList && items.length === 0 && emptyMessage && (
        <p className={`muted ${mpStyles["mp-rail-empty"]}`}>{emptyMessage}</p>
      )}
      {!useCustomContent && footer && <div className={mpStyles["mp-rail-footer"]}>{footer}</div>}
    </div>
  );
}
