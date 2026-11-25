import { computed, ref, type Ref } from "vue";
import { Message } from "@arco-design/web-vue";
import { queryTimeSeriesRows } from "@/api/storage/view";
import type { FilterSpec, TimeRange, TimeSeriesKey, TimeSeriesRow } from "@/api/storage/types";
import { createLatestRequestGuard } from "@/utils/latest-request";
import {
  buildKlineChartRecords,
  buildKlineQuerySorts,
  buildViewFilterExprs,
  DEFAULT_KLINE_LIMIT,
  exactSeriesTagFromFilters,
  klineRowsHaveFreq,
  klineSubjectIdFromFilters,
  normalizeKlineLimit,
  timeSeriesRowsToTableRows,
  type BrowseTableRow,
  type KlineChartRecord,
  type ViewFilterState
} from "../view-browse-utils";

export type ViewBrowseTableRow = BrowseTableRow & { freq?: string; rawKey?: TimeSeriesKey };

export const KLINE_PAGE_SIZE = 25;

interface KlineQuerySnapshot {
  filter: FilterSpec | undefined;
  columnNames: string[];
  sorts: ReturnType<typeof buildKlineQuerySorts>;
  limit: number;
}

export interface UseViewKlineOptions {
  spaceId: () => string;
  datasetId: () => string | undefined;
  zone: () => string;
  filters: Ref<ViewFilterState[]>;
  /** 当前数据集里画 K 线要的列（open_time、open、high、low、close、volume 中存在的）。 */
  columnNames: () => string[];
  /** 查询区自己的筛选条件（不含调用方附加的条件）。 */
  ownFilterExprs: () => FilterSpec | undefined;
  previewRange: () => TimeRange | undefined;
}

/** 浏览页的 K 线取数：工具栏按查询区的标的与序列标签打开，行操作按那一行打开。 */
export function useViewKline(options: UseViewKlineOptions) {
  const klineVisible = ref(false);
  const klineSubjectId = ref("");
  const klineFreq = ref("");
  const klineRecords = ref<KlineChartRecord[]>([]);
  const klineLoading = ref(false);
  const klineLimit = ref(DEFAULT_KLINE_LIMIT);
  const normalizedKlineLimit = computed(() => normalizeKlineLimit(klineLimit.value));

  // 从行操作打开的 K 线：用那一行的标的和序列标签筛，不用查询区的筛选；从工具栏打开时为空。
  const klineRowFilter = ref<{ subjectId: string; seriesTag: string } | null>(null);

  // K 线加载只服务于发起时的数据集：切换数据集/空间、关闭弹窗或卸载都会使在途请求失效，迟到的响应不能重新打开弹窗或写入旧数据
  const klineRequestGuard = createLatestRequestGuard();

  function rowKlineFilter(subjectId: string, seriesTag: string) {
    return buildViewFilterExprs(
      [
        { fieldName: "subject_id", operator: "eq", value: subjectId },
        seriesTag ? { fieldName: "series_tag", operator: "eq", value: seriesTag } : { fieldName: "series_tag", operator: "empty" }
      ],
      options.zone()
    );
  }

  /** 行操作“K 线”：直接用这一行的标的和序列标签打开图表。 */
  async function openRowKline(record: { rawKey?: { subject_id?: string; series_tag?: string } }) {
    const subjectId = record.rawKey?.subject_id;
    if (!subjectId) return;
    const seriesTag = record.rawKey?.series_tag ?? "";
    klineRowFilter.value = { subjectId, seriesTag };
    klineSubjectId.value = subjectId;
    const ok = await loadKlineRecords(subjectId, seriesTag, rowKlineFilter(subjectId, seriesTag));
    if (ok) klineVisible.value = true;
  }

  async function openKlineModal() {
    klineRowFilter.value = null;
    const subjectId = klineSubjectIdFromFilters(options.filters.value);
    if (!subjectId) {
      Message.warning("请先在数据ID检索框输入要查看的标的");
      return;
    }
    const seriesTag = exactSeriesTagFromFilters(options.filters.value);
    if (seriesTag === undefined) {
      Message.warning("请先精确选择序列标签；默认序列请输入 default");
      return;
    }

    klineSubjectId.value = subjectId;
    const ok = await loadKlineRecords(subjectId, seriesTag);
    if (!ok) return;
    klineVisible.value = true;
  }

  async function reloadKlineRecords() {
    const row = klineRowFilter.value;
    if (row) {
      await loadKlineRecords(row.subjectId, row.seriesTag, rowKlineFilter(row.subjectId, row.seriesTag));
      return;
    }
    const subjectId = klineSubjectId.value || klineSubjectIdFromFilters(options.filters.value);
    if (!subjectId) {
      Message.warning("请先在数据ID检索框输入要查看的标的");
      return;
    }
    const seriesTag = exactSeriesTagFromFilters(options.filters.value);
    if (seriesTag === undefined) {
      Message.warning("请先精确选择序列标签；默认序列请输入 default");
      return;
    }
    klineSubjectId.value = subjectId;
    await loadKlineRecords(subjectId, seriesTag);
  }

  async function loadKlineRecords(subjectId: string, seriesTag: string, filterOverride?: FilterSpec) {
    const datasetId = options.datasetId();
    if (!datasetId) return false;
    const request = klineRequestGuard.begin();
    // 发起时固定整次查询的筛选、投影列、排序与数量：加载期间修改筛选框不能让后续分页读取另一套条件
    const query: KlineQuerySnapshot = {
      filter: filterOverride ?? options.ownFilterExprs(),
      columnNames: [...options.columnNames()],
      sorts: buildKlineQuerySorts(),
      limit: normalizedKlineLimit.value
    };
    klineLoading.value = true;
    try {
      const rows = await fetchKlineTableRows(datasetId, query, () => request.isLatest());
      if (!request.isLatest()) return false;
      if (!klineRowsHaveFreq(rows)) {
        Message.warning("当前查询结果缺少 freq 字段，无法展示K线");
        return false;
      }

      const records = buildKlineChartRecords(rows, subjectId, seriesTag);
      if (records.length === 0) {
        Message.warning("当前结果缺少 open/high/low/close 字段，无法生成K线图");
        return false;
      }

      klineLimit.value = query.limit;
      klineFreq.value = firstKlineFreq(subjectId, rows);
      klineRecords.value = records;
      return true;
    } catch (error) {
      if (!request.isLatest()) return false;
      Message.error(error instanceof Error ? error.message : "加载K线数据失败");
      return false;
    } finally {
      if (request.isLatest()) klineLoading.value = false;
    }
  }

  async function fetchKlineTableRows(
    datasetId: string,
    query: KlineQuerySnapshot,
    isCurrent: () => boolean
  ): Promise<ViewBrowseTableRow[]> {
    const space_id = options.spaceId();
    const timeRange = options.previewRange();
    const rows: TimeSeriesRow[] = [];
    const limit = query.limit;
    for (let pageNo = 1; rows.length < limit; pageNo += 1) {
      const rsp = await queryTimeSeriesRows({
        space_id,
        dataset_id: datasetId,
        ...(timeRange ? { time_range: timeRange } : {}),
        ...(query.columnNames.length > 0 ? { column_names: query.columnNames } : {}),
        filter: query.filter,
        sorts: query.sorts,
        limit,
        page: { page: pageNo, size: KLINE_PAGE_SIZE },
        total_mode: "NONE"
      });
      if (!isCurrent()) break; // 已失效：不再翻页
      const pageRows = rsp.rows || [];
      rows.push(...pageRows);
      if (!rsp.page_result?.has_more || pageRows.length === 0) break;
    }
    return timeSeriesRowsToTableRows(rows).map((row, index) => ({
      ...row,
      freq: rows[index]?.key?.freq || "-",
      seriesTag: rows[index]?.key?.series_tag || "",
      rawKey: rows[index]?.key
    }));
  }

  function firstKlineFreq(subjectId: string, rows: ViewBrowseTableRow[]) {
    const row =
      rows.find(item => item.key === subjectId && item.freq && item.freq !== "-") ||
      rows.find(item => item.freq && item.freq !== "-");
    return row?.freq || "-";
  }

  function closeKlineModal() {
    klineRequestGuard.invalidate();
    klineLoading.value = false;
    klineVisible.value = false;
    klineRecords.value = [];
    klineSubjectId.value = "";
    klineFreq.value = "";
  }

  return {
    klineVisible,
    klineSubjectId,
    klineFreq,
    klineRecords,
    klineLoading,
    klineLimit,
    openRowKline,
    openKlineModal,
    reloadKlineRecords,
    closeKlineModal,
    invalidateKline: () => klineRequestGuard.invalidate()
  };
}
