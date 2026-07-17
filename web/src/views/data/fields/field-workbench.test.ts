import { describe, expect, it } from "vitest";
import type { FieldGroup } from "@/api/storage/types";
import { buildGroupTree, fieldQueryFromRoute, fieldQueryToRoute, groupPath, RequestGate } from "./field-workbench";

const groups: FieldGroup[] = [
  { space_id: "stock_cn", group_id: "market", name: "市场数据", status: "active", sort_order: 20 },
  { space_id: "stock_cn", group_id: "quote", parent_group_id: "market", name: "行情价格", status: "active", sort_order: 10 },
  { space_id: "stock_cn", group_id: "identity", name: "标识信息", status: "active", sort_order: 10 }
];

describe("field workbench helpers", () => {
  it("builds a stable two-level group tree", () => {
    const tree = buildGroupTree(groups);
    expect(tree.map(item => item.group_id)).toEqual(["identity", "market"]);
    expect(tree[1].children.map(item => item.group_id)).toEqual(["quote"]);
  });

  it("formats the complete group path", () => {
    expect(groupPath(groups, "quote")).toBe("市场数据 / 行情价格");
    expect(groupPath(groups, "missing")).toBe("missing");
  });

  it("normalizes route query values and serializes defaults sparsely", () => {
    const state = fieldQueryFromRoute({ group: "quote", keyword: " close ", page: "3", page_size: "50", order: "desc" });
    expect(state).toMatchObject({ group: "quote", keyword: "close", page: 3, pageSize: 50, sort: "sort_order", order: "desc" });
    expect(fieldQueryToRoute(state)).toEqual({ group: "quote", keyword: "close", page: "3", page_size: "50", order: "desc" });
  });

  it("rejects stale request tokens", () => {
    const gate = new RequestGate();
    const first = gate.next();
    const second = gate.next();
    expect(gate.isCurrent(first)).toBe(false);
    expect(gate.isCurrent(second)).toBe(true);
  });
});
