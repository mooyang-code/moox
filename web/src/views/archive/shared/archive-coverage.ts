/**
 * 采集任务页用的“是否已归档”：当前空间读一次归档任务和数据集，按数据集给出“已归档 · 健康”或“未归档”。
 * - 同一空间共用一份模块级缓存，有效期 60 秒；切空间、归档页建任务或删任务后失效。
 * - 任一读取失败（包括 archive-mgr 没部署）时什么都不显示，不能把读不到当成“未归档”。
 */
import { computed, shallowRef, watch, type Ref } from "vue";
import { readAllPages } from "@/api/read-all-pages";
import { listArchiveTasks, type ArchiveTask } from "@/api/archive";
import { listDatasets } from "@/api/storage/metadata";
import type { Dataset } from "@/api/storage/types";
import { archivableDataset, healthLabel, healthTone, type Tone } from "./archive-model";

const TTL_MS = 60_000;

export interface ArchiveCoverageData {
  spaceId: string;
  loadedAt: number;
  tasks: Map<string, ArchiveTask>;
  /** 应该归档的数据集（时序、active、有限保留期） */
  archivable: Set<string>;
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
    const [taskRsp, datasets] = await Promise.all([listArchiveTasks({ page: 1, size: 100 }), listAllDatasets(spaceId)]);
    return {
      spaceId,
      loadedAt: nowMs,
      tasks: new Map((taskRsp.tasks || []).map(task => [task.dataset_id || "", task])),
      archivable: new Set(datasets.filter(item => archivableDataset(item)).map(item => item.dataset_id))
    };
  } catch {
    return null;
  }
}

/** 一个数据集的归档状态；没有可显示的（永久保留、不是时序、读取失败、dataset_id 为空）时为 null。 */
export function coverageFor(data: ArchiveCoverageData | null, datasetId?: string): ArchiveCoverage | null {
  if (!data || !datasetId) return null;
  const task = data.tasks.get(datasetId);
  if (task) {
    return {
      text: `已归档 · ${healthLabel(task.health)}`,
      tone: task.health === "ok" ? "muted" : healthTone(task.health),
      link: `/archive/tasks?task=${encodeURIComponent(datasetId)}`
    };
  }
  return data.archivable.has(datasetId)
    ? { text: "未归档", tone: "warn", link: `/archive/tasks?create=${encodeURIComponent(datasetId)}` }
    : null;
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
