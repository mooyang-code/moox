import { describe, expect, it, vi } from "vitest";
import { TradeResponseError } from "@/api/trade";
import type { ExchangeInspection, TradingAccount } from "@/api/trade/types";
import { fromInspection } from "./inspection";
import { previewAllowsNext, previewState, runPreview, runReinspect, type InspectionApi } from "./inspection-actions";

const passing: ExchangeInspection = { account_mode: "BINANCE_STANDARD", can_trade_spot: true, available_markets: [1] };
const blocked: ExchangeInspection = {
  account_mode: "BINANCE_STANDARD",
  can_withdraw: true,
  blockers: [{ code: "withdraw_permission", message: "密钥带有提现权限" }]
};

function api(overrides: Partial<InspectionApi>): InspectionApi {
  return {
    inspect: vi.fn().mockResolvedValue({ inspection: passing }),
    reinspect: vi
      .fn()
      .mockResolvedValue({ inspection: passing, account: { trading_account_id: "a" } as TradingAccount, mode_changed: false }),
    ...overrides
  };
}

describe("新建账户的检测预览", () => {
  it("检测通过才放行，有阻断项、测试网跳过的按各自状态，没有结论和还没检测都不放行", () => {
    expect(previewState(fromInspection(passing), false, false)).toBe("ok");
    expect(previewState(fromInspection(blocked), false, false)).toBe("blocked");
    expect(previewState(fromInspection({ account_mode: "BINANCE_STANDARD", skipped: true }), false, false)).toBe("skipped");
    expect(previewState(null, true, false)).toBe("running");
    expect(previewState(null, false, true)).toBe("unavailable");
    expect(previewState(null, false, false)).toBe("idle");
    expect(["ok", "skipped"].every(state => previewAllowsNext(state as never))).toBe(true);
    for (const state of ["idle", "running", "blocked", "unavailable"] as const) expect(previewAllowsNext(state)).toBe(false);
  });

  it("检测返回阻断项时不放行；传输错误不下结论，也不放行", async () => {
    const passed = await runPreview({ exchange: 1, environment: 2, credential_secret_id: "s" }, api({}));
    expect(passed.state).toBe("ok");
    const withBlocker = await runPreview(
      { exchange: 1, environment: 2, credential_secret_id: "s" },
      api({ inspect: vi.fn().mockResolvedValue({ inspection: blocked }) })
    );
    expect(withBlocker.state).toBe("blocked");
    expect(withBlocker.view?.blockers[0].message).toContain("提现");
    const unavailable = await runPreview(
      { exchange: 1, environment: 2, credential_secret_id: "s" },
      api({ inspect: vi.fn().mockRejectedValue(new Error("trade account: 密钥检测没有完成")) })
    );
    expect(unavailable.state).toBe("unavailable");
    expect(unavailable.view).toBeNull();
    expect(unavailable.message).toContain("没有完成");
    expect(previewAllowsNext(unavailable.state)).toBe(false);
  });
});

describe("已有账户的重新检测", () => {
  it("成功时带回新结果和账户；账户类型变化给出提示", async () => {
    const outcome = await runReinspect("a", api({}));
    expect(outcome.ok).toBe(true);
    expect(outcome.account?.trading_account_id).toBe("a");
    expect(outcome.message).toBe("");
    const changed = await runReinspect(
      "a",
      api({ reinspect: vi.fn().mockResolvedValue({ inspection: passing, account: {} as TradingAccount, mode_changed: true }) })
    );
    expect(changed.modeChanged).toBe(true);
    expect(changed.message).toContain("账户类型发生了变化");
  });

  it("检测没完成时界面仍然刷新：用后端保留的上次结果和累加的失败次数，并说明这次没检测成", async () => {
    const retained: ExchangeInspection = { ...passing, consecutive_failures: 2, last_failure: "HTTP 503" };
    const error = new TradeResponseError({
      ret_info: { code: 4, msg: "trade account: 密钥检测没有完成" },
      ...({ inspection: retained, account: { trading_account_id: "a" } } as object)
    });
    const outcome = await runReinspect("a", api({ reinspect: vi.fn().mockRejectedValue(error) }));
    expect(outcome.ok).toBe(false);
    expect(outcome.view?.consecutiveFailures).toBe(2);
    expect(outcome.account?.trading_account_id).toBe("a");
    expect(outcome.message).toContain("保留上次的结果");
    const plain = await runReinspect("a", api({ reinspect: vi.fn().mockRejectedValue(new Error("network")) }));
    expect(plain.ok).toBe(false);
    expect(plain.view).toBeNull();
  });
});
