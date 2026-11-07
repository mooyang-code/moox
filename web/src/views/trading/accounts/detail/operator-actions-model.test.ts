import { describe, expect, it } from "vitest";
import type { OperatorAction } from "@/api/trade";
import {
  operatorActionHint,
  operatorActionTone,
  operatorActionTypeOptions,
  operatorActionTypeText
} from "./operator-actions-model";

const action = (result_json: string): OperatorAction => ({
  action_id: "a-1",
  logical_account_id: "l-1",
  action_type: "DELIST_FLATTEN",
  reason: "",
  status: "COMPLETED",
  result_json,
  last_error: "",
  created_at: "1",
  updated_at: "1"
});

describe("人工操作记录", () => {
  it("六种类型都有中文名，未知类型原样显示", () => {
    expect(operatorActionTypeOptions.map(item => item.label)).toEqual([
      "人工下单",
      "提交订单",
      "撤单",
      "整组清仓",
      "按比例减仓",
      "下架清仓"
    ]);
    expect(operatorActionTypeText("REDUCE")).toBe("按比例减仓");
    expect(operatorActionTypeText("NEW_TYPE")).toBe("NEW_TYPE");
    expect(operatorActionTypeText("")).toBe("未知");
  });

  it("状态对应的颜色：执行中是运行态，部分完成告警，失败为红", () => {
    expect(operatorActionTone("RUNNING")).toBe("run");
    expect(operatorActionTone("COMPLETED")).toBe("ok");
    expect(operatorActionTone("PARTIAL")).toBe("warn");
    expect(operatorActionTone("FAILED")).toBe("bad");
  });

  it("结果里 unblocked 为真时提示“已解除禁止”，其余内容不展示", () => {
    expect(operatorActionHint(action('{"unblocked":true,"accounts":[]}'))).toContain("解除禁止");
    expect(operatorActionHint(action('{"unblocked":false}'))).toBe("");
    expect(operatorActionHint(action('{"unblocked":"true"}'))).toBe("");
    expect(operatorActionHint(action('{"accounts":[]}'))).toBe("");
  });

  it("结果为空、不是 JSON 或不是对象时没有提示，也不抛错", () => {
    expect(operatorActionHint(action(""))).toBe("");
    expect(operatorActionHint(action("not json"))).toBe("");
    expect(operatorActionHint(action("null"))).toBe("");
    expect(operatorActionHint(action("[true]"))).toBe("");
  });
});
