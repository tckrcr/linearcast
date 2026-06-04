import type { ReactNode } from "react";
import { MediaPickerRail, type PickerRailItem } from "./MediaPickerRail";

// SchedulePickerRailTab describes one tab in the rail. The default rendering
// uses MediaPickerRail's built-in filter input + list; pass `content` instead
// to render a custom body (e.g. a poster grid) while keeping the tab bar.
export type SchedulePickerRailTab = {
  id: string;
  label: string;
  // When `content` is set, the rail renders this slot below the tab bar and
  // skips its own filter/list rendering. The other fields are ignored.
  content?: ReactNode;
  query?: string;
  onQueryChange?: (q: string) => void;
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
  items?: PickerRailItem[];
  onItemAction?: (key: string) => void;
  itemActionBusy?: boolean;
  emptyMessage?: string;
  footer?: ReactNode;
  defaultActionLabel?: string;
};

export type SchedulePickerRailProps = {
  title?: ReactNode;
  subtitle?: ReactNode;
  onClose?: () => void;
  tabs: SchedulePickerRailTab[];
  activeTab: string;
  onTabChange: (tab: string) => void;
};

export function SchedulePickerRail({
  title,
  subtitle,
  onClose,
  tabs,
  activeTab,
  onTabChange,
}: SchedulePickerRailProps) {
  const active = tabs.find((tab) => tab.id === activeTab) ?? tabs[0];
  if (!active) return null;

  return (
    <MediaPickerRail
      title={title}
      subtitle={subtitle}
      onClose={onClose}
      tabs={tabs.map((tab) => ({ id: tab.id, label: tab.label }))}
      activeTab={active.id}
      onTabChange={onTabChange}
      query={active.query ?? ""}
      onQueryChange={active.onQueryChange ?? (() => {})}
      queryPlaceholder={active.queryPlaceholder}
      searchDisabled={active.searchDisabled}
      onRefresh={active.onRefresh}
      refreshing={active.refreshing}
      refreshLabel={active.refreshLabel}
      loading={active.loading}
      loadingMessage={active.loadingMessage}
      error={active.error}
      notice={active.notice}
      toolsExtra={active.toolsExtra}
      items={active.items ?? []}
      onItemAction={active.onItemAction ?? (() => {})}
      itemActionBusy={active.itemActionBusy}
      emptyMessage={active.emptyMessage}
      footer={active.footer}
      defaultActionLabel={active.defaultActionLabel}
      content={active.content}
    />
  );
}
