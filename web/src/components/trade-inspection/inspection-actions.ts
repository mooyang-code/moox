import { inspectExchangeCredential, reinspectTradingAccount, TradeResponseError } from "@/api/trade";
import type { ExchangeInspection, InspectExchangeCredentialReq, TradingAccount } from "@/api/trade/types";
import { fromInspection, inspectionVerdict, type InspectionView } from "./inspection";

// 预览（新建账户第二步、选完密钥立即检测）与已有账户的重新检测。接口作为参数传入，便于测试。

export type PreviewState = "idle" | "running" | "ok" | "skipped" | "blocked" | "unavailable";

export interface PreviewResult {
  state: PreviewState;
  view: InspectionView | null;
  /** 检测没完成（超时、限频等）时的提示；没有结论不代表密钥有问题。 */
  message: string;
}

/** 由检测结果推出预览状态：有阻断项是 blocked，测试网跳过是 skipped，其余是 ok。 */
export function previewState(view: InspectionView | null, running: boolean, unavailable: boolean): PreviewState {
  if (running) return "running";
  if (unavailable) return "unavailable";
  if (!view) return "idle";
  const verdict = inspectionVerdict(view);
  return verdict === "ok" ? "ok" : verdict;
}

/** 新建账户第二步能不能点“下一步”：检测通过（含测试网跳过）才行，没有结论也不放行。 */
export function previewAllowsNext(state: PreviewState): boolean {
  return state === "ok" || state === "skipped";
}

export interface InspectionApi {
  inspect: (req: InspectExchangeCredentialReq) => Promise<{ inspection: ExchangeInspection }>;
  reinspect: (accountId: string) => Promise<{ inspection: ExchangeInspection; account: TradingAccount; mode_changed: boolean }>;
}

export const defaultInspectionApi: InspectionApi = {
  inspect: inspectExchangeCredential,
  reinspect: reinspectTradingAccount
};

function errorText(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}

export async function runPreview(
  request: InspectExchangeCredentialReq,
  api: InspectionApi = defaultInspectionApi
): Promise<PreviewResult> {
  try {
    const rsp = await api.inspect(request);
    const view = fromInspection(rsp.inspection);
    return { state: previewState(view, false, false), view, message: "" };
  } catch (error) {
    // 检测没有完成（传输错误、限频）：不下结论，不放行，提示稍后重试。
    return { state: "unavailable", view: null, message: errorText(error, "检测失败，请稍后重试") };
  }
}

export interface ReinspectOutcome {
  /** 检测成功（有结论）。 */
  ok: boolean;
  /** 本次检测之后要显示的结果；检测没完成时是后端保留的上次结果。 */
  view: InspectionView | null;
  account: TradingAccount | null;
  modeChanged: boolean;
  message: string;
}

/** 重新检测一个已有账户。检测没完成时后端保留上次结果，只累加失败次数；这里把保留的结果和账户一并带回，界面照常刷新。 */
export async function runReinspect(accountId: string, api: InspectionApi = defaultInspectionApi): Promise<ReinspectOutcome> {
  try {
    const rsp = await api.reinspect(accountId);
    return {
      ok: true,
      view: fromInspection(rsp.inspection),
      account: rsp.account ?? null,
      modeChanged: !!rsp.mode_changed,
      message: rsp.mode_changed ? "账户类型发生了变化，请确认后再恢复组合" : ""
    };
  } catch (error) {
    const response = (error instanceof TradeResponseError ? error.response : undefined) as
      | { inspection?: ExchangeInspection; account?: TradingAccount }
      | undefined;
    return {
      ok: false,
      view: response?.inspection ? fromInspection(response.inspection) : null,
      account: response?.account ?? null,
      modeChanged: false,
      message: `这次没有检测成功，保留上次的结果：${errorText(error, "请稍后重试")}`
    };
  }
}
