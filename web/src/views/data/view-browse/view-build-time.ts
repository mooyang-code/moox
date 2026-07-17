import type { View } from "@/api/storage/types";

function formatTimestamp(value?: string) {
  if (!value) return "";
  const date = new Date(value);
  return Number.isNaN(date.valueOf()) ? "" : date.toLocaleString("zh-CN");
}

export function viewBuildTimeLabel(view?: View) {
  const build = view?.index_build;
  if (!view?.active_index_id || !build || build.index_id !== view.active_index_id) return "构建时间未知";
  const finished = formatTimestamp(build.finished_at);
  if (finished) return `构建完成 · ${finished}`;
  const started = formatTimestamp(build.started_at);
  if (started) return `构建中 · 开始于 ${started}`;
  return "构建时间未知";
}
