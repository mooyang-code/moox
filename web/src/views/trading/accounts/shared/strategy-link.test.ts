import { describe, expect, it, vi } from "vitest";
import type { StrategyInstance } from "@/api/strategy-types";

const api = vi.hoisted(() => ({ listInstances: vi.fn() }));
vi.mock("@/api/strategy", () => api);
import { buildStrategyLinks, loadStrategyLinks } from "./strategy-link";

const instance = (id: string, extra: Partial<StrategyInstance> = {}) =>
  ({
    instance_id: id,
    strategy_id: `st-${id}`,
    strategy_name: "",
    logical_account_id: "la",
    enabled: false,
    health: "ok",
    created_at: "1767225600000",
    ...extra
  }) as StrategyInstance;
const link = (list: StrategyInstance[]) => buildStrategyLinks([{ logical_account_id: "la" }], list).get("la");

describe("关联策略取哪个实例", () => {
  it("取绑了这个账户且启用中的实例，哪怕它不在最前", () => {
    const list = [
      instance("new", { strategy_id: "st-new", strategy_name: "新策略" }),
      instance("live", { strategy_id: "st-live", strategy_name: "运行中的策略", enabled: true })
    ];
    expect(link(list)).toMatchObject({ instanceId: "live", name: "运行中的策略", enabled: true });
  });

  it("只看 Strategy 的实例列表：绑了别的账户的启用实例不算", () => {
    const list = [
      instance("other", { strategy_id: "st-live", strategy_name: "运行中的策略", logical_account_id: "la-other", enabled: true }),
      instance("old", { strategy_id: "st-old", strategy_name: "旧策略" })
    ];
    expect(link(list)).toMatchObject({ instanceId: "old", name: "旧策略", enabled: false });
  });

  it("都没启用时取最新创建的，不因列表顺序留下最老的", () => {
    const list = [
      instance("old", { strategy_id: "st-old", strategy_name: "旧策略", created_at: "1767225600000" }),
      instance("new", { strategy_id: "st-new", strategy_name: "新策略", created_at: "1772323200000" })
    ];
    expect(link(list)?.instanceId).toBe("new");
    expect(link([...list].reverse())?.instanceId).toBe("new");
  });

  it("没有实例引用时不返回；策略名称为空时退回策略编号", () => {
    expect(link([])).toBeUndefined();
    expect(link([instance("x", { strategy_id: "st-unknown", strategy_name: "" })])?.name).toBe("st-unknown");
  });
});

describe("读取关联策略", () => {
  it("读取失败时返回 null，页面据此写“策略读取失败”而不是“尚未绑定策略”", async () => {
    api.listInstances.mockRejectedValueOnce(new Error("策略服务不可用"));
    expect(await loadStrategyLinks([])).toBeNull();
  });
});
