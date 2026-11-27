import { computed, inject, provide, ref, type InjectionKey } from "vue";
import { Message } from "@arco-design/web-vue";
import { storeToRefs } from "pinia";
import { GetDataTypeConfigs, GetDataTypeConfigWithFields, GetTaskList, type DataTypeConfig } from "@/api/collector";
import { listTags } from "@/api/storage/metadata";
import type { Tag } from "@/api/storage/types";
import { useSpaceStore } from "@/store/modules/space";
import { tagMarketType, tagSource } from "@/views/data/shared/catalog-attributes";
import {
  buildOutputFieldCatalog,
  normalizeCollectionTask,
  taskFrequency,
  type CollectionTaskRecord,
  type OutputFieldEntry
} from "./collection-task-params";

/**
 * 采集任务页几块共用的数据：数据类型、标签、可作合成源的任务、各数据类型的输出字段目录。
 * 列表页创建并 provide，编辑抽屉、详情抽屉 inject 同一份，切空间时由列表页统一重载。
 */
function createCollectionCatalog() {
  const spaceStore = useSpaceStore();
  const { selectedSpaceId } = storeToRefs(spaceStore);

  const dataTypeConfigs = ref<DataTypeConfig[]>([]);
  const availableDataTypeConfigs = computed(() =>
    dataTypeConfigs.value.filter(config => !["instrument", "symbol"].includes(config.data_type))
  );
  const tags = ref<Tag[]>([]);
  const loadingTags = ref(false);
  const sourceTasks = ref<CollectionTaskRecord[]>([]);
  const loadingSourceTasks = ref(false);
  /** 数据类型 → 输出字段目录（基础输出字段与附加字段，带中文名与分组）。不随空间变化。 */
  const outputFieldCatalogs = ref<Record<string, OutputFieldEntry[]>>({});

  let tagListRequestToken = 0;
  let sourceListRequestToken = 0;

  async function loadDataTypeConfigs() {
    try {
      const response = await GetDataTypeConfigs();
      dataTypeConfigs.value = response.configs || [];
    } catch (error) {
      Message.error(error instanceof Error ? error.message : "获取数据类型失败");
    }
  }

  async function loadTags() {
    const spaceID = selectedSpaceId.value;
    const token = ++tagListRequestToken;
    if (!spaceID) {
      tags.value = [];
      return;
    }
    loadingTags.value = true;
    try {
      const allTags: Tag[] = [];
      for (let page = 1; ; page += 1) {
        const response = await listTags(spaceID, { page, size: 500 });
        if (token !== tagListRequestToken || spaceID !== selectedSpaceId.value) return;
        allTags.push(...(response.tags || []));
        if (!response.page_result?.has_more || !(response.tags || []).length) break;
      }
      if (token !== tagListRequestToken || spaceID !== selectedSpaceId.value) return;
      tags.value = allTags;
    } catch (error) {
      if (token === tagListRequestToken) {
        tags.value = [];
        Message.error(error instanceof Error ? error.message : "获取标签失败");
      }
    } finally {
      if (token === tagListRequestToken) loadingTags.value = false;
    }
  }

  async function loadSourceTasks() {
    const spaceId = selectedSpaceId.value;
    const token = ++sourceListRequestToken;
    if (!spaceId) {
      sourceTasks.value = [];
      loadingSourceTasks.value = false;
      return;
    }
    loadingSourceTasks.value = true;
    try {
      const tasks: CollectionTaskRecord[] = [];
      for (let page = 1; ; page += 1) {
        const response = await GetTaskList({ space_id: spaceId, page: { page, size: 200 } });
        // 整次分页固定发起空间：切换空间或有更新的请求后，旧响应整体丢弃
        if (token !== sourceListRequestToken || spaceId !== selectedSpaceId.value) return;
        const batch = (response.tasks || []).map(normalizeCollectionTask);
        tasks.push(...batch.filter(task => task.data_type === "kline" || task.data_type === "kline_resample"));
        if (!response.page?.has_more || batch.length === 0) break;
      }
      sourceTasks.value = tasks;
    } catch (error) {
      if (token !== sourceListRequestToken) return;
      sourceTasks.value = [];
      Message.error(error instanceof Error ? error.message : "获取源采集任务失败");
    } finally {
      if (token === sourceListRequestToken) loadingSourceTasks.value = false;
    }
  }

  /** 读取一个数据类型的参数与字段规格，顺便记下它的输出字段目录。 */
  async function loadDataTypeDetail(dataType: string) {
    const response = await GetDataTypeConfigWithFields(dataType);
    const detail = response.detail;
    outputFieldCatalogs.value = {
      ...outputFieldCatalogs.value,
      [dataType]: buildOutputFieldCatalog(detail?.base_output_fields, detail?.extra_fields)
    };
    return detail;
  }

  /** 详情抽屉显示字段中文名用：目录读过就不再读，读不到时显示字段 ID。 */
  async function ensureOutputFieldCatalog(dataType: string) {
    if (!dataType || outputFieldCatalogs.value[dataType]) return;
    try {
      await loadDataTypeDetail(dataType);
    } catch {
      // 只影响显示名，失败时显示字段 ID。
    }
  }

  /** 切换空间：作废在途的标签与源任务请求，清掉旧空间的数据。 */
  function resetForSpace() {
    tagListRequestToken += 1;
    sourceListRequestToken += 1;
    sourceTasks.value = [];
  }

  function dataTypeLabel(value?: string) {
    return dataTypeConfigs.value.find(config => config.data_type === value)?.type_name || value || "-";
  }

  function tagsForTask(task: CollectionTaskRecord): Tag[] {
    const ids = new Set(task.tag_ids || []);
    return tags.value.filter(tag => ids.has(tag.tag_id));
  }

  function taskTagSummary(task: CollectionTaskRecord): string {
    const matched = tagsForTask(task);
    if (matched.length) return matched.map(tag => tag.tag_name || tag.tag_id).join("、");
    return (task.tag_ids || []).join("、") || "-";
  }

  function taskRouteSummary(task: CollectionTaskRecord, visited = new Set<string>()): string {
    const routes = [...new Set(tagsForTask(task).map(tag => `${tagSource(tag) || "-"} / ${tagMarketType(tag) || "-"}`))];
    if (routes.length) return routes.join("；");
    if (task.data_type === "kline_resample" && !visited.has(task.task_id)) {
      visited.add(task.task_id);
      const sourceId = stringParam(task, "source_task_id");
      const source = sourceTasks.value.find(item => item.task_id === sourceId);
      if (source) return taskRouteSummary(source, visited);
    }
    return "-";
  }

  function stringParam(task: CollectionTaskRecord, key: string): string {
    const value = task.collect_params?.[key];
    return typeof value === "string" ? value.trim() : "";
  }

  // 列表“合成来源”列：源任务名 → 目标频率；老任务没有 source_task_id 时显示源数据集。
  function resampleSourceSummary(task: CollectionTaskRecord): string {
    if (task.data_type !== "kline_resample") return "-";
    const sourceId = stringParam(task, "source_task_id");
    const source = sourceTasks.value.find(item => item.task_id === sourceId);
    const from = source ? source.task_name || source.task_id : sourceId || stringParam(task, "source_dataset_id") || "-";
    return `${from} → ${taskFrequency(task)}`;
  }

  /** 以 task 为源的合成任务。 */
  function dependentsOf(task: CollectionTaskRecord | undefined) {
    return task ? sourceTasks.value.filter(item => stringParam(item, "source_task_id") === task.task_id) : [];
  }

  return {
    selectedSpaceId,
    dataTypeConfigs,
    availableDataTypeConfigs,
    tags,
    loadingTags,
    sourceTasks,
    loadingSourceTasks,
    outputFieldCatalogs,
    loadDataTypeConfigs,
    loadTags,
    loadSourceTasks,
    loadDataTypeDetail,
    ensureOutputFieldCatalog,
    resetForSpace,
    dataTypeLabel,
    taskTagSummary,
    taskRouteSummary,
    stringParam,
    resampleSourceSummary,
    dependentsOf
  };
}

export type CollectionCatalog = ReturnType<typeof createCollectionCatalog>;

const catalogKey: InjectionKey<CollectionCatalog> = Symbol("collection-catalog");

/** 列表页调用：创建并提供共用数据。 */
export function provideCollectionCatalog(): CollectionCatalog {
  const catalog = createCollectionCatalog();
  provide(catalogKey, catalog);
  return catalog;
}

/** 抽屉调用：取列表页提供的共用数据。 */
export function useCollectionCatalog(): CollectionCatalog {
  const catalog = inject(catalogKey);
  if (!catalog) throw new Error("采集任务页的共用数据未提供");
  return catalog;
}
