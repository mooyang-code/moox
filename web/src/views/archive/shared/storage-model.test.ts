import { describe, expect, it } from "vitest";
import type { ComponentDeployment, DeploymentHost } from "@/api/admin/types";
import {
  archiveQueryFor,
  avoidanceBasisText,
  avoidanceScheduleText,
  dirNameError,
  dirNameOwner,
  diskStatus,
  downloadBlockedReason,
  hostText,
  hourQuietText,
  isBuiltinObjectStore,
  minuteWindowText,
  parseArchiveQuery,
  partitionPathPreview,
  recentCellText,
  resolveWorkerHost,
  workerHostText
} from "./archive-model";

const GiB = 1024 ** 3;

describe("分区下载按钮", () => {
  it("只有远端已校验可下载，其余给出原因", () => {
    expect(downloadBlockedReason({ remote_state: "verified" })).toBe("");
    expect(downloadBlockedReason({ remote_state: "pending" })).toBe("远端副本还没有上传或校验");
    expect(downloadBlockedReason({ remote_state: "pending", remote_key: "k" })).toBe("远端只有旧版本，当前版本还没有上传或校验");
    expect(downloadBlockedReason({ remote_state: "failed", remote_error: "超时" })).toBe("远端副本上传失败：超时");
    expect(downloadBlockedReason({ remote_state: "none" })).toBe("还没有远端副本");
    expect(downloadBlockedReason({ remote_state: "off" })).toContain("没有配置对象存储");
  });
});

describe("执行器所在主机", () => {
  const host = (over: Partial<DeploymentHost> = {}): DeploymentHost => ({
    host_id: "factor",
    label: "因子机",
    address: "192.168.0.103",
    status: "enabled",
    ...over
  });
  const deployment = (over: Partial<ComponentDeployment> = {}): ComponentDeployment => ({
    host_id: "factor",
    component_id: "archive-worker",
    status: "enabled",
    ...over
  });

  it("唯一启用的部署且主机启用：名称（地址）", () => {
    const info = resolveWorkerHost([deployment(), deployment({ host_id: "control", status: "disabled" })], [host()]);
    expect(info.state).toBe("ok");
    expect(workerHostText(info)).toBe("因子机（192.168.0.103）");
  });

  it("没有、多条、主机停用或不在登记里", () => {
    expect(resolveWorkerHost([], [host()]).state).toBe("none");
    expect(resolveWorkerHost([deployment({ component_id: "collector" })], [host()]).state).toBe("none");
    expect(
      resolveWorkerHost([deployment(), deployment({ host_id: "control" })], [host(), host({ host_id: "control" })]).state
    ).toBe("many");
    const stopped = resolveWorkerHost([deployment()], [host({ status: "disabled" })]);
    expect(stopped).toEqual({ state: "unknown", hostId: "factor" });
    expect(workerHostText(stopped)).toBe("factor");
    expect(workerHostText(null)).toBe("");
  });

  it("名称缺省用主机 ID，地址缺省用内网地址", () => {
    expect(hostText({ host_id: "h1", address: "", private_address: "10.0.0.2" })).toBe("h1（10.0.0.2）");
    expect(hostText({ host_id: "h1", address: "" })).toBe("h1");
  });
});

describe("磁盘", () => {
  it("已用 / 总量、可用；差额是 root 保留空间", () => {
    const disk = diskStatus({
      disk_total_bytes: String(100 * GiB),
      disk_used_bytes: String(30 * GiB),
      disk_free_bytes: String(65 * GiB),
      min_free_bytes: String(5 * GiB)
    });
    expect(disk?.text).toBe("已用 30 GB / 共 100 GB，可用 65 GB");
    expect(disk?.insufficient).toBe(false);
    expect(disk?.title).toContain("比总量少 5 GB，是文件系统给 root 的保留空间");
  });

  it("可用低于下限时不足，和 worker 用同一个数", () => {
    const disk = diskStatus({
      disk_total_bytes: 100 * GiB,
      disk_used_bytes: 96 * GiB,
      disk_free_bytes: 4 * GiB,
      min_free_bytes: 5 * GiB
    });
    expect(disk?.insufficient).toBe(true);
    expect(disk?.warning).toBe("磁盘空间不足（可用低于下限 5 GB），已暂停领取新运行");
  });

  it("下限没上报时不判断；总量为 0 时不显示", () => {
    expect(diskStatus({ disk_total_bytes: 100, disk_free_bytes: 0 })?.insufficient).toBe(false);
    expect(diskStatus({ disk_total_bytes: 0 })).toBeNull();
    expect(diskStatus(undefined)).toBeNull();
  });
});

describe("对象存储", () => {
  it("内置 SeaweedFS 按地址判断", () => {
    expect(isBuiltinObjectStore("127.0.0.1:11473")).toBe(true);
    expect(isBuiltinObjectStore("http://127.0.0.1:11473/")).toBe(true);
    expect(isBuiltinObjectStore("cos.ap-shanghai.myqcloud.com")).toBe(false);
    expect(isBuiltinObjectStore("")).toBe(false);
  });
});

describe("避让时段", () => {
  const base = { before_seconds: 5, after_seconds: 25 };
  it.each([
    [{ ...base, every_minutes: 0 }, "不限秒"],
    [{ ...base, every_minutes: 1 }, "每分钟只在第 25～55 秒读取"],
    [{ ...base, every_minutes: 5 }, "每 5 分钟的触发时刻前 5 秒到后 25 秒不读取"]
  ])("每分钟规则 %j", (rules, text) => {
    expect(minuteWindowText(rules)).toBe(text);
  });

  it("整点窗口：有时写区间和顺延时刻，没有时不顺延", () => {
    const quiet = { ...base, every_minutes: 1, hour_quiet_from: "58:00", hour_quiet_until: "04:00" };
    expect(hourQuietText(quiet)).toBe("每小时 :58:00～:04:00 不拉取");
    expect(avoidanceScheduleText(quiet)).toBe(
      "每分钟第 25 秒检查；每小时 :58:00～:04:00 不拉取，这段时间的边界顺延到 :04:25；执行器读取 Storage 时每分钟只在第 25～55 秒读取。"
    );
    expect(avoidanceScheduleText({ ...quiet, hour_quiet_until: "04:30" })).toContain("顺延到 :05:25");
    const none = { ...base, every_minutes: 5 };
    expect(hourQuietText(none)).toBe("不避让整点");
    expect(avoidanceScheduleText(none)).toContain("不避让整点，边界到了就拉取");
    expect(avoidanceScheduleText(undefined)).toBe("");
  });

  it("依据：没算出过时写默认值", () => {
    expect(avoidanceBasisText({ basis: "crypto：1m" })).toBe("crypto：1m");
    expect(avoidanceBasisText({})).toBe("未取到采集频率，使用默认值");
  });
});

describe("目录名", () => {
  it.each([
    ["binance-spot-1m", ""],
    ["", "请填写目录名"],
    ["a".repeat(65), "目录名最长 64 个字符"],
    ["Binance", "只能用小写字母、数字和单个连字符，不能以连字符开头或结尾"],
    ["a--b", "只能用小写字母、数字和单个连字符，不能以连字符开头或结尾"],
    ["-a", "只能用小写字母、数字和单个连字符，不能以连字符开头或结尾"],
    ["a_b", "只能用小写字母、数字和单个连字符，不能以连字符开头或结尾"]
  ])("%s", (name, error) => {
    expect(dirNameError(name)).toBe(error);
  });

  it("路径预览和 worker 的 RelPath 一致：日分区文件名是年月日，月分区是年月", () => {
    expect(partitionPathPreview("crypto", "collect", "binance-spot-kline-1m", "day")).toBe(
      "crypto/collect/binance-spot-kline-1m/<yyyy>/<yyyymmdd>.csv.gz"
    );
    expect(partitionPathPreview("crypto", "factor", "x-1d", "month")).toBe("crypto/factor/x-1d/<yyyy>/<yyyymm>.csv.gz");
    expect(partitionPathPreview("crypto", "", "", "day")).toBe("crypto/<用途>/<目录名>/<yyyy>/<yyyymmdd>.csv.gz");
    // 有归档根目录时写成绝对路径
    expect(partitionPathPreview("crypto", "collect", "a-1m", "day", "/data/moox/archive/")).toBe(
      "/data/moox/archive/crypto/collect/a-1m/<yyyy>/<yyyymmdd>.csv.gz"
    );
  });

  it("占用目录名的任务按用途与目录名找", () => {
    const tasks = [
      { dataset_id: "a", purpose: "collect", dir_name: "x-1m" },
      { dataset_id: "b", purpose: "factor", dir_name: "x-1m" }
    ];
    expect(dirNameOwner(tasks, "factor", "x-1m")?.dataset_id).toBe("b");
    expect(dirNameOwner(tasks, "collect", "y-1m")).toBeUndefined();
  });
});

describe("最新数据", () => {
  it("缺失值和空字符串分开显示，时间按空间时区", () => {
    expect(recentCellText(null, "double", "UTC")).toBe("—");
    expect(recentCellText("", "string", "UTC")).toBe('""');
    expect(recentCellText("1.5", "double", "UTC")).toBe("1.5");
    expect(recentCellText("2026-11-07T04:40:00Z", "time", "Asia/Shanghai")).toBe("2026-11-07 12:40:00 UTC+8");
  });

  it("页签和标的进 URL", () => {
    const query = parseArchiveQuery({ task: "ds", tab: "recent", subject: "BTC-USDT" });
    expect(query).toMatchObject({ tab: "recent", subject: "BTC-USDT" });
    expect(archiveQueryFor(query)).toEqual({ task: "ds", tab: "recent", subject: "BTC-USDT" });
    // 其他页签不写标的
    expect(archiveQueryFor({ ...query, tab: "runs" })).toEqual({ task: "ds" });
  });
});
