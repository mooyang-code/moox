import type { Page, Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

// 数据归档页的 mock：拦截 /api/admin/** 的全部调用，只用本地数据，不会触达任何归档服务。
const ok = (data: Record<string, unknown> = {}) => ({ ret_info: { code: 0, msg: "success" }, ...data });
const NOW = Date.now();
const iso = (ms: number) => new Date(ms).toISOString().replace(/\.\d{3}Z$/, "Z");
const MINUTE = 60_000;
const DAY = 86_400_000;

const datasets = [
  {
    dataset_id: "ds_kline_1m",
    name: "现货 1 分钟 K 线",
    data_kind: "DATA_KIND_TIME_SERIES",
    freq: "1m",
    status: "active",
    retention: "840h"
  },
  {
    dataset_id: "ds_kline_5m",
    name: "现货 5 分钟 K 线",
    data_kind: "DATA_KIND_TIME_SERIES",
    freq: "5m",
    status: "active",
    retention: "840h"
  },
  { dataset_id: "ds_subjects", name: "标的清单", data_kind: "DATA_KIND_RECORD", status: "active", retention: "forever" }
];

const task = (datasetId: string, over: Record<string, unknown> = {}) => ({
  space_id: "crypto",
  dataset_id: datasetId,
  freq: "1m",
  partition_unit: "day",
  retention: "840h",
  pull_every_bars: 1,
  interval_seconds: 3600,
  status: "enabled",
  next_pull_at: iso(NOW + 2 * MINUTE),
  last_success_at: iso(NOW - 30 * MINUTE),
  health: "ok",
  health_reason: "",
  partition_count: 3,
  file_count: 3,
  total_file_bytes: "1048576",
  min_data_time: iso(NOW - 3 * DAY),
  max_data_time: iso(NOW - MINUTE),
  last_run: {
    run_id: "run-1",
    kind: "scheduled",
    status: "succeeded",
    started_at: iso(NOW - 30 * MINUTE),
    finished_at: iso(NOW - 29 * MINUTE)
  },
  ...over
});

const runs = [
  {
    run_id: "run-q",
    dataset_id: "ds_kline_1m",
    kind: "tick",
    status: "queued",
    scheduled_at: iso(NOW),
    range_start: iso(NOW - 2 * MINUTE),
    range_end: iso(NOW)
  },
  {
    run_id: "run-1",
    dataset_id: "ds_kline_1m",
    kind: "scheduled",
    status: "succeeded",
    scheduled_at: iso(NOW - 30 * MINUTE),
    started_at: iso(NOW - 30 * MINUTE),
    finished_at: iso(NOW - 29 * MINUTE),
    rows_read: "1440",
    rows_added: "60",
    rows_changed: "0",
    partitions_exported: 1,
    partitions_finalized: 0
  }
];

const partitions = [
  {
    partition_key: "2026-11-07",
    status: "open",
    row_count: "600",
    subject_count: "10",
    min_data_time: iso(NOW - DAY),
    max_data_time: iso(NOW),
    file_path: "crypto/ds_kline_1m/2026/20261107.csv.gz",
    file_bytes: "2048",
    file_sha256: "a".repeat(64),
    export_count: 1
  }
];

export interface ArchiveCalls {
  /** 各方法收到的请求体 */
  bodies: Record<string, Record<string, unknown>[]>;
  /** 为 true 时归档接口一律返回 500 */
  fail: boolean;
}
export let calls: ArchiveCalls;
let tasks: ReturnType<typeof task>[] = [];

async function gateway(route: Route) {
  const method = route.request().url().split("/").pop()?.split("?")[0] || "";
  const body = (route.request().postDataJSON?.() || {}) as Record<string, unknown>;
  (calls.bodies[method] ||= []).push(body);
  const archive = route.request().url().includes("/api/admin/archive/");
  if (archive && calls.fail) return route.fulfill({ status: 500, body: "archive-mgr unavailable" });
  switch (method) {
    case "GetUserInfo":
      return route.fulfill({
        json: ok({ user_info: { user_id: "e2e", username: "reviewer", nickname: "Reviewer", role: 3, status: 1 } })
      });
    case "ListSpaces":
      return route.fulfill({
        json: ok({
          spaces: [{ space_id: "crypto", name: "Crypto", owner: "e2e", status: "active", timezone: "UTC" }],
          page_result: { total: 1 }
        })
      });
    case "ListDatasets":
      return route.fulfill({
        json: ok({ datasets, page_result: { page: 1, size: 200, total: datasets.length, has_more: false } })
      });
    case "GetArchiveOverview":
      return route.fulfill({
        json: ok({
          worker: {
            worker_id: "archive-worker@factor",
            active: true,
            last_heartbeat_at: iso(NOW - 12_000),
            disk_free_bytes: "1900000000000",
            disk_total_bytes: "2000000000000",
            staging_bytes: "12288"
          },
          running_runs: [],
          queued_run_count: 1,
          alerts: []
        })
      });
    case "ListArchiveTasks":
      return route.fulfill({ json: ok({ tasks, page_result: { total: tasks.length } }) });
    case "PrepareArchiveTask":
      return route.fulfill({
        json: ok({
          freq: "5m",
          partition_unit: "day",
          retention: "840h",
          earliest_backfill_from: iso(NOW - 34 * DAY),
          recommended_pull_every_bars: 1,
          exists: tasks.some(item => item.dataset_id === body.dataset_id)
        })
      });
    case "PreviewArchiveSchedule":
      return route.fulfill({
        json: ok({
          next_runs: [1, 2, 3, 4, 5].map(i => iso(NOW + i * 5 * MINUTE)),
          next_reviews: [1, 2, 3].map(i => iso(NOW + i * 60 * MINUTE)),
          interval_seconds: 3600,
          read_span_seconds: 4560,
          partition_export_estimates: [iso(NOW + 10 * MINUTE), iso(NOW + DAY)]
        })
      });
    case "CreateArchiveTask":
      tasks = [
        ...tasks,
        task(String(body.dataset_id), {
          freq: "5m",
          partition_count: 0,
          min_data_time: "",
          max_data_time: "",
          last_run: undefined
        })
      ];
      return route.fulfill({ json: ok({ task: tasks.at(-1) }) });
    case "ListArchiveRuns":
      return route.fulfill({ json: ok({ runs, page_result: { total: runs.length } }) });
    case "CancelArchiveRun":
      return route.fulfill({ json: ok({ run: { ...runs[0], status: "canceled" } }) });
    case "ListArchivePartitions":
      return route.fulfill({ json: ok({ partitions, page_result: { total: partitions.length } }) });
    case "GetArchivePartition":
      return route.fulfill({
        json: ok({
          partition: { ...partitions[0], columns: ["subject_id", "close"], preview: "subject_id,close\nBTC-USDT,64000\n" }
        })
      });
    default:
      return route.fulfill({ json: ok() });
  }
}

export async function installArchiveMock(page: Page) {
  calls = { bodies: {}, fail: false };
  tasks = [task("ds_kline_1m")];
  await installE2ESession(page, "crypto");
  await page.route(/\/api\/admin\/[^/]+\/[^/?#]+(?:\?|$)/, gateway);
  return calls;
}
