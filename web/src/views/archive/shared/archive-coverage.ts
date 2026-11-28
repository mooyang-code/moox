/**
 * 采集任务页用的“是否已归档”：当前空间读一次归档任务，已有任务时给出“已归档 · 健康”。
 * 归档任务已由 ArchiveMgr 自动补齐，前端不再维护“未归档数据集”判定与提示。
 * - 同一空间共用一份模块级缓存，有效期 60 秒；切空间、归档页建任务或删任务后失效。
 * - 读取失败（包括 archive-mgr 没部署）时什么都不显示。
 */
import { computed, shallowRef, watch, type Ref } from "vue";
import { readAllPages } from "@/api/read-all-pages";
import { listArchiveTasks, type ArchiveTask } from "@/api/archive";
import { listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { healthLabel, healthTone, type Tone } from "./archive-model";

const TTL_MS = 60_000;

export interface ArchiveCoverageData {
  spaceId: string;
  loadedAt: number;
  tasks: Map<string, ArchiveTask>;
}

export interface ArchiveCoverage {
  text: string;
  tone: Tone;
  link: string;
}

/** 当前空间的全部数据集（翻完所有页）。 */
export function listAllDatasets(spaceId: string): Promise<Dataset[]> {
  let read = 0;
  return readAllPages(async page => {
    const rsp = await listDatasets({ space_id: spaceId, page: { page, size: 200 } });
    const items = rsp.datasets || [];
    read += items.length;
    return { items, hasMore: Boolean(rsp.page_result?.has_more) || read < Number(rsp.page_result?.total || 0) };
  });
}

export async function loadArchiveCoverage(spaceId: string, nowMs = Date.now()): Promise<ArchiveCoverageData | null> {
  try {
    const taskRsp = await listArchiveTasks({ page: 1, size: 100 });
    return {
      spaceId,
      loadedAt: nowMs,
      tasks: new Map((taskRsp.tasks || []).map(task => [task.dataset_id || "", task]))
    };
  } catch {
    return null;
  }
}

/** 一个数据集的归档状态；没有对应归档任务、读取失败或 dataset_id 为空时为 null。 */
export function coverageFor(data: ArchiveCoverageData | null, datasetId?: string): ArchiveCoverage | null {
  if (!data || !datasetId) return null;
  const task = data.tasks.get(datasetId);
  if (!task) return null;
  return {
    text: `已归档 · ${healthLabel(task.health)}`,
    tone: task.health === "ok" ? "muted" : healthTone(task.health),
    link: `/archive/tasks?task=${encodeURIComponent(datasetId)}`
  };
}

// ---- 模块级缓存 ----

const cache = shallowRef<ArchiveCoverageData | null>(null);
let inflight: { spaceId: string; promise: Promise<void> } | null = null;

/** 归档页建任务、删任务成功后调用：回到采集任务页时重读。 */
export function invalidateArchiveCoverage() {
  cache.value = null;
  inflight = null;
}

async function ensureCoverage(spaceId: string) {
  if (!spaceId) return;
  const current = cache.value;
  if (current && current.spaceId === spaceId && Date.now() - current.loadedAt < TTL_MS) return;
  if (inflight?.spaceId === spaceId) return inflight.promise;
  const promise = loadArchiveCoverage(spaceId).then(data => {
    // 读取期间切了空间或被作废：结果丢弃
    if (inflight?.promise !== promise) return;
    inflight = null;
    cache.value = data;
  });
  inflight = { spaceId, promise };
  return promise;
}

/** 在组件里用：切空间时重读，返回按数据集取归档状态的函数。 */
export function useArchiveCoverage(spaceId: Ref<string>) {
  watch(spaceId, value => void ensureCoverage(value), { immediate: true });
  return computed(() => {
    const data = cache.value && cache.value.spaceId === spaceId.value ? cache.value : null;
    return (datasetId?: string) => coverageFor(data, datasetId);
  });
}
