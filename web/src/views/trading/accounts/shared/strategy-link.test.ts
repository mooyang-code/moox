import { describe, expect, it, vi } from "vitest";
import type { StrategyInstance } from "@/api/strategy-types";

const api = vi.hoisted(() => ({ listInstances: vi.fn(), listStrategies: vi.fn() }));
vi.mock("@/api/strategy", () => api);
import { buildStrategyLinks, loadStrategyLinks } from "./strategy-link";

const instance = (id: string, extra: Partial<StrategyInstance> = {}) =>
  ({
    instance_id: id,
    strategy_id: `st-${id}`,
    logical_account_id: "la",
    enabled: false,
    health: "ok",
    created_at: "2026-01-01T00:00:00Z",
    ...extra
  }) as StrategyInstance;
const names = new Map([
  ["st-old", "旧策略"],
  ["st-new", "新策略"],
  ["st-live", "运行中的策略"]
]);
const link = (list: StrategyInstance[], owner = "") =>
  buildStrategyLinks([{ logical_account_id: "la", owner_instance_id: owner }], list, names).get("la");

describe("关联策略取哪个实例", () => {
  it("优先取组合自己记着的实例，哪怕它不在最前、也没启用", () => {
    const list = [instance("new", { strategy_id: "st-new", enabled: true }), instance("old", { strategy_id: "st-old" })];
    expect(link(list, "old")).toMatchObject({ instanceId: "old", name: "旧策略" });
  });

  it("没有记着的实例时取启用中的", () => {
    const list = [instance("new", { strategy_id: "st-new" }), instance("live", { strategy_id: "st-live", enabled: true })];
    expect(link(list)).toMatchObject({ instanceId: "live", name: "运行中的策略" });
  });

  it("都没启用时取最新创建的，不因列表顺序留下最老的", () => {
    const list = [
      instance("old", { strategy_id: "st-old", created_at: "2026-01-01T00:00:00Z" }),
      instance("new", { strategy_id: "st-new", created_at: "2026-03-01T00:00:00Z" })
    ];
    expect(link(list)?.instanceId).toBe("new");
    expect(link([...list].reverse())?.instanceId).toBe("new");
  });

  it("没有实例引用时不返回；策略定义查不到时退回策略编号", () => {
    expect(link([])).toBeUndefined();
    expect(link([instance("x", { strategy_id: "st-unknown" })])?.name).toBe("st-unknown");
  });
});

describe("读取关联策略", () => {
  it("读取失败时返回 null，页面据此写“策略读取失败”而不是“尚未绑定策略”", async () => {
    api.listInstances.mockRejectedValueOnce(new Error("策略服务不可用"));
    api.listStrategies.mockResolvedValueOnce({ items: [] });
    expect(await loadStrategyLinks([])).toBeNull();
  });
});
