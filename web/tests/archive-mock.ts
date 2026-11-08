import type { Page, Route } from "@playwright/test";
import { installE2ESession } from "./e2e-session";

// 数据归档（归档任务、归档存储）页的 mock：拦截 /api/admin/** 的全部调用，只用本地数据，不会触达任何归档服务。
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
  purpose: "collect",
  dir_name: "binance-spot-kline-1m",
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
    file_path: "crypto/collect/binance-spot-kline-1m/2026/20261107.csv.gz",
    file_bytes: "2048",
    file_sha256: "a".repeat(64),
    export_count: 1,
    remote_state: "verified",
    remote_key: "moox-archive/crypto/collect/binance-spot-kline-1m/2026/20261107.csv.gz"
  },
  {
    partition_key: "2026-11-08",
    status: "open",
    row_count: "60",
    subject_count: "10",
    min_data_time: iso(NOW - MINUTE),
    max_data_time: iso(NOW),
    file_path: "crypto/collect/binance-spot-kline-1m/2026/20261108.csv.gz",
    file_bytes: "512",
    file_sha256: "b".repeat(64),
    export_count: 1,
    remote_state: "pending"
  }
];

const GiB = 1024 ** 3;
const recentColumns = [
  { name: "data_time", type: "time" },
  { name: "subject_id", type: "string" },
  { name: "series_tag", type: "string" },
  { name: "close", type: "double" },
  { name: "note", type: "string" }
];
const recentRows = (before: string | null) => {
  const end = before ? Date.parse(before) : Math.floor(NOW / MINUTE) * MINUTE + MINUTE;
  return Array.from({ length: 3 }, (_, i) => [
    iso(end - (i + 1) * MINUTE),
    "BTC-USDT",
    "",
    String(64000 + i),
    i === 0 ? null : ""
  ]);
};

export interface ArchiveCalls {
  /** 各方法收到的请求体 */
  bodies: Record<string, Record<string, unknown>[]>;
  /** 为 true 时归档接口一律返回 500 */
  fail: boolean;
}
export let calls: ArchiveCalls;
let tasks: ReturnType<typeof task>[] = [];

async function gateway(route: Route) {
  const url = new URL(route.request().url());
  const method = url.pathname.split("/").pop() || "";
  const body = (route.request().postDataJSON?.() || Object.fromEntries(url.searchParams)) as Record<string, unknown>;
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
            root_dir: "/data/moox/archive",
            version: "v0.6.0",
            instance_id: "inst-1",
            disk_total_bytes: String(1800 * GiB),
            disk_used_bytes: String(137 * GiB),
            disk_free_bytes: String(1570 * GiB),
            min_free_bytes: String(5 * GiB),
            staging_bytes: "12288",
            object_store: { endpoint: "127.0.0.1:11473", bucket: "moox-archive", prefix: "moox-archive/", url_style: "path" }
          },
          running_runs: [],
          queued_run_count: 1,
          alerts: [],
          avoidance: {
            every_minutes: 1,
            before_seconds: 5,
            after_seconds: 25,
            hour_quiet_from: "58:00",
            hour_quiet_until: "04:00",
            basis: "crypto：1m、1h 等（每分钟触发，有 1h 及以上）",
            computed_at: iso(NOW - 5 * MINUTE)
          }
        })
      });
    case "ListDeployments":
      return route.fulfill({
        json: ok({
          deployments: [{ host_id: "factor", component_id: "archive-worker", status: "enabled" }],
          page_result: { total: 1, has_more: false }
        })
      });
    case "ListHosts":
      return route.fulfill({
        json: ok({
          hosts: [{ host_id: "factor", label: "因子机", address: "192.168.0.103", status: "enabled", reachability: "outbound" }],
          page_result: { total: 1, has_more: false }
        })
      });
    case "ListSubjects":
      return route.fulfill({
        json: ok({
          subjects: [{ space_id: "crypto", subject_id: "BTC-USDT", name: "BTC-USDT", status: "active" }],
          page_result: { total: 1 }
        })
      });
    case "IssueRawSessionTicket":
      return route.fulfill({ json: ok({ ticket: `ticket-${(calls.bodies[method] || []).length}`, expires_at: 0 }) });
    case "RecentRows":
      return route.fulfill({
        json: { columns: recentColumns, rows: recentRows(url.searchParams.get("before")), staged_until: iso(NOW - MINUTE) }
      });
    case "PartitionDownload":
      return route.fulfill({
        status: 200,
        headers: {
          "Content-Type": "application/gzip",
          "Content-Disposition": `attachment; filename="binance-spot-kline-1m_${url.searchParams.get("partition")}.csv.gz"`
        },
        body: Buffer.from([0x1f, 0x8b, 0x08, 0x00])
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
          exists: tasks.some(item => item.dataset_id === body.dataset_id),
          purpose: "collect",
          suggested_dir_name: "binance-spot-kline-5m",
          // 已有任务的目录名算冲突
          dir_name_conflict: tasks.some(item => item.dir_name === (body.dir_name || "binance-spot-kline-5m"))
        })
      });
    case "PreviewArchiveSchedule":
      return route.fulfill({
        json: ok({
          next_runs: [1, 2, 3, 4, 5].map(i => iso(NOW + i * 5 * MINUTE)),
          next_reviews: [1, 2, 3].map(i => iso(NOW + i * 60 * MINUTE)),
          interval_seconds: 3600,
          read_span_seconds: 4560,
          // 后端给的是文本，不是时刻
          partition_export_estimates: [
            `分区 2026-11-06：预计 ${iso(NOW + 10 * MINUTE)} 导出`,
            `分区 2026-11-07：预计 ${iso(NOW + DAY)} 导出`
          ]
        })
      });
    case "CreateArchiveTask":
      tasks = [
        ...tasks,
        task(String(body.dataset_id), {
          freq: "5m",
          dir_name: String(body.dir_name || ""),
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
