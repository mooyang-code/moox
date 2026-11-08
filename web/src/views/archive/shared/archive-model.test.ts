import { describe, expect, it } from "vitest";
import type { ArchiveTask } from "@/api/archive";
import type { Dataset } from "@/api/storage/types";
import {
  archiveQueryFor,
  archiveUnavailableHint,
  backfillFromError,
  backfillRangeError,
  durationText,
  exportEstimateText,
  healthLabel,
  kindLabel,
  parseArchiveQuery,
  parsePreviewCsv,
  partitionStatusLabel,
  pauseConfirmText,
  pauseSafeDays,
  pullEveryBarsError,
  pullText,
  remoteStateLabel,
  reviewText,
  runDurationText,
  runStatusLabel,
  showBackfillField,
  systemAlerts,
  toNum,
  unarchivedDatasets
} from "./archive-model";

describe("文案", () => {
  it("运行类型、状态、健康等级、分区状态", () => {
    expect(["scheduled", "manual", "tick", "range", "import"].map(kindLabel)).toEqual([
      "复核",
      "立即运行",
      "到点拉取",
      "补拉区间",
      "导入"
    ]);
    expect(["queued", "running", "succeeded", "failed", "canceled"].map(runStatusLabel)).toEqual([
      "排队中",
      "运行中",
      "成功",
      "失败",
      "已取消"
    ]);
    expect(["ok", "warning", "error", "paused"].map(healthLabel)).toEqual(["正常", "需关注", "异常", "已停用"]);
    expect(["open", "exported", "final"].map(partitionStatusLabel)).toEqual(["暂存中", "已导出", "已定稿"]);
  });

  it("远端副本五种状态", () => {
    expect(remoteStateLabel("verified")).toBe("已校验");
    expect(remoteStateLabel("pending")).toBe("待上传");
    expect(remoteStateLabel("pending", "old/key")).toBe("待上传（远端只有旧版本）");
    expect(remoteStateLabel("failed")).toBe("失败");
    expect(remoteStateLabel("none")).toBe("—");
    expect(remoteStateLabel("off")).toBe("");
  });
});

describe("拉取与复核", () => {
  const task = (over: Partial<ArchiveTask>): ArchiveTask => ({
    freq: "1m",
    pull_every_bars: 1,
    status: "enabled",
    next_pull_at: "2026-11-07T04:41:25Z",
    ...over
  });

  it("按 N 写每收盘几根，下次拉取到分钟、带空间时区后缀", () => {
    expect(pullText(task({}), "UTC")).toBe("每收盘 1 根 · 下次 2026-11-07 04:41 UTC");
    expect(pullText(task({ pull_every_bars: 5 }), "Asia/Shanghai")).toBe("每收盘 5 根 · 下次 2026-11-07 12:41 UTC+8");
    expect(pullText(task({ freq: "1h" }), "UTC")).toBe("每收盘 1 根 · 下次 2026-11-07 04:41 UTC");
    expect(pullText(task({ freq: "1d" }), "UTC")).toBe("每天收盘后 · 下次 2026-11-07 04:41 UTC");
    expect(pullText(task({ status: "paused", next_pull_at: "" }), "UTC")).toBe("已停用");
  });

  it("复核规律：低于 1d 每小时第 34 分，1d 及以上拉取即复核", () => {
    expect(reviewText(task({}))).toBe("每小时第 34 分复核");
    expect(reviewText(task({ freq: "1d" }))).toBe("拉取即复核");
  });
});

describe("时长", () => {
  it("秒、分、时、天", () => {
    expect(durationText(45)).toBe("45 秒");
    expect(durationText(133)).toBe("2 分 13 秒");
    expect(durationText(120)).toBe("2 分");
    expect(durationText(4560)).toBe("1 小时 16 分");
    expect(durationText(2 * 86400 + 3 * 3600)).toBe("2 天 3 小时");
  });

  it("运行耗时：运行中按现在算，没开始或没结束（已结束状态）写 —", () => {
    const now = Date.parse("2026-11-07T00:10:00Z");
    expect(runDurationText({ started_at: "2026-11-07T00:00:00Z", finished_at: "2026-11-07T00:02:13Z" }, now)).toBe("2 分 13 秒");
    expect(runDurationText({ started_at: "2026-11-07T00:00:00Z", status: "running" }, now)).toBe("10 分");
    expect(runDurationText({ status: "queued" }, now)).toBe("—");
    expect(runDurationText({ started_at: "2026-11-07T00:00:00Z", status: "failed" }, now)).toBe("—");
  });
});

describe("未归档的数据集", () => {
  const dataset = (id: string, over: Partial<Dataset> = {}) =>
    ({ dataset_id: id, data_kind: "DATA_KIND_TIME_SERIES", status: "active", retention: "840h", ...over }) as Dataset;

  it("和 Monitor 同口径：时序、active（不分大小写）、有限保留期、不是系统空间，再减去已有任务", () => {
    const datasets = [
      dataset("a"),
      dataset("b", { data_kind: 2 }),
      dataset("c", { status: "ACTIVE" }),
      dataset("d", { data_kind: "DATA_KIND_RECORD" }),
      dataset("e", { status: "disabled" }),
      dataset("f", { retention: "forever" }),
      dataset("g", { retention: "" }),
      dataset("h")
    ];
    expect(unarchivedDatasets(datasets, [{ dataset_id: "h" }], "crypto").map(item => item.dataset_id)).toEqual(["a", "b", "c"]);
    expect(unarchivedDatasets(datasets, [], "mooxsys")).toEqual([]);
  });

  it("提醒里只去掉和任务健康原因完全相同的（任务表已显示），其余以任务 ID 开头的照常显示", () => {
    const tasks = [{ dataset_id: "ds_1m", health_reason: "最近一次成功运行距今 30 天" }];
    expect(
      systemAlerts(
        [
          "ds_1m：最近一次成功运行距今 30 天",
          "ds_1m：分区登记在 worker a，当前活动 worker 是 b，运行不会派给它",
          "没有活动的归档 worker"
        ],
        tasks
      )
    ).toEqual(["ds_1m：分区登记在 worker a，当前活动 worker 是 b，运行不会派给它", "没有活动的归档 worker"]);
  });

  it("分区预计导出时间：解析后端的文本，时刻按空间时区；读不懂时原样", () => {
    expect(exportEstimateText("分区 2026-11-06：预计 2026-11-07T00:15:25Z 导出", "Asia/Shanghai")).toBe(
      "分区 2026-11-06 预计 2026-11-07 08:15:25 UTC+8 导出"
    );
    expect(exportEstimateText("别的格式", "UTC")).toBe("别的格式");
  });
});

describe("校验", () => {
  it("拉取间隔：1～1440 的整数，频率 × N 不超过 1 天；1d 不校验", () => {
    expect(pullEveryBarsError("1m", 0)).not.toBe("");
    expect(pullEveryBarsError("1m", 1441)).not.toBe("");
    expect(pullEveryBarsError("1m", 1440)).toBe("");
    expect(pullEveryBarsError("5m", 288)).toBe("");
    expect(pullEveryBarsError("5m", 289)).toContain("最多 288 根");
    expect(pullEveryBarsError("1d", 0)).toBe("");
  });

  it("回补起点和补拉区间：不早于下界、不晚于现在、起点早于终点", () => {
    const now = Date.parse("2026-11-07T00:00:00Z");
    const earliest = Date.parse("2026-10-04T00:00:00Z");
    expect(backfillFromError(null, earliest, now, "UTC")).toBe("");
    expect(backfillFromError(earliest, earliest, now, "UTC")).toBe("");
    expect(backfillFromError(earliest - 1, earliest, now, "UTC")).toContain("不能早于 2026-10-04 00:00 UTC");
    // 提示里的时间按空间时区
    expect(backfillFromError(earliest - 1, earliest, now, "Asia/Shanghai")).toContain("不能早于 2026-10-04 08:00 UTC+8");
    expect(backfillFromError(now + 1, earliest, now, "UTC")).toBe("不能晚于现在");
    expect(backfillRangeError(null, earliest, now, "UTC")).toBe("请选择补拉区间");
    expect(backfillRangeError([now - 10, now - 10], earliest, now, "UTC")).toBe("结束时间必须晚于开始时间");
    expect(backfillRangeError([earliest - 1, now], earliest, now, "UTC")).toContain("开始不能早于");
    expect(backfillRangeError([earliest, now + 1], earliest, now, "UTC")).toBe("结束不能晚于现在");
    expect(backfillRangeError([earliest, now], earliest, now, "UTC")).toBe("");
  });

  it("首次复核成功后编辑时不再显示回补起点", () => {
    expect(showBackfillField({ last_success_at: "" })).toBe(true);
    expect(showBackfillField({ last_success_at: "2026-11-07T00:34:00Z" })).toBe(false);
  });

  it("停用安全天数和健康等级的异常门槛同一公式", () => {
    expect(pauseSafeDays("840h")).toBe(33);
    expect(pauseSafeDays("72h")).toBe(1);
    expect(pauseSafeDays("forever")).toBeNull();
    expect(pauseConfirmText("840h", "35 天")).toContain("线上只保留 35 天：停用超过约 33 天");
    expect(pauseConfirmText("forever", "永久")).toBe("停用后不再拉取和复核。重新启用后会立即复核。");
  });

  it("uint64 字段是字符串或数字", () => {
    expect(toNum("12")).toBe(12);
    expect(toNum(3)).toBe(3);
    expect(toNum(undefined)).toBe(0);
    expect(toNum("x")).toBe(0);
  });
});

describe("分区预览 CSV", () => {
  it("引号、引号内的逗号和换行、转义的引号、空字段", () => {
    const text = 'subject_id,note,close\n"BTC-USDT","a,b",1\nETH-USDT,"line1\nline2",\n"X","say ""hi""",3\n';
    expect(parsePreviewCsv(text)).toEqual({
      header: ["subject_id", "note", "close"],
      rows: [
        ["BTC-USDT", "a,b", "1"],
        ["ETH-USDT", "line1\nline2", ""],
        ["X", 'say "hi"', "3"]
      ]
    });
  });

  it("空串、不闭合的引号返回 null；列数不对的行去掉", () => {
    expect(parsePreviewCsv("")).toBeNull();
    expect(parsePreviewCsv('a,b\n"x,1')).toBeNull();
    expect(parsePreviewCsv("a,b\n1,2\n3")?.rows).toEqual([["1", "2"]]);
  });
});

describe("URL 状态", () => {
  it("非法值规范化", () => {
    expect(parseArchiveQuery({ tab: "x", status: "bad", rpage: "0", from: "2026-13", ticks: "yes" })).toMatchObject({
      tab: "runs",
      status: "",
      rpage: 1,
      from: "",
      ticks: false
    });
  });

  it("往返一致，默认值不写进 URL，没打开详情时不写详情参数", () => {
    const runs = { task: "ds_1m", tab: "runs", status: "failed", ticks: true, rpage: 2 } as const;
    expect(archiveQueryFor(parseArchiveQuery(archiveQueryFor(runs) as never))).toEqual({
      task: "ds_1m",
      status: "failed",
      ticks: "1",
      rpage: "2"
    });
    const parts = {
      task: "ds_1m",
      tab: "partitions",
      from: "2026-10",
      to: "2026-11",
      ppage: 1,
      partition: "2026-11-07"
    } as const;
    expect(archiveQueryFor(parts)).toEqual({
      task: "ds_1m",
      tab: "partitions",
      from: "2026-10",
      to: "2026-11",
      partition: "2026-11-07"
    });
    expect(archiveQueryFor({ tab: "partitions", status: "failed" })).toEqual({});
    expect(archiveQueryFor({ create: "ds_5m" })).toEqual({ create: "ds_5m" });
  });
});

describe("读取失败的说明", () => {
  it("像是没部署时补一句 archive-mgr 是可选组件", () => {
    expect(archiveUnavailableHint("Request failed with status code 404")).toContain("可选组件");
    expect(archiveUnavailableHint("connect ECONNREFUSED 127.0.0.1:11450")).toContain("可选组件");
    expect(archiveUnavailableHint("数据库忙")).toBe("");
  });
});
