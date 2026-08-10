import type { View } from "@/api/storage/types";

function formatTimestamp(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "" : date.toLocaleString("zh-CN");
}

export function viewBuildTimeLabel(view?: View) {
  const build = view?.index_build;
  if (!view?.active_index_id) return "构建时间未知";
  if (build?.index_id === view.active_index_id) {
    const started = formatTimestamp(build.started_at);
    const finished = formatTimestamp(build.finished_at);
    if (finished && started) return `构建完成 · 开始 ${started} · 完成 ${finished}`;
    if (finished) return `构建完成 · ${finished}`;
    if (started) return `构建中 · 开始 ${started}`;
  }
  // Storage retires the successful A/B build record after activation. The
  // View's persisted update time is the durable timestamp available for the
  // currently active index, so show it instead of an unhelpful "unknown".
  const activeTimestamp = formatTimestamp(view.updated_at || view.created_at);
  if (activeTimestamp) return `构建完成 · ${activeTimestamp}`;
  return "构建时间未知";
}
