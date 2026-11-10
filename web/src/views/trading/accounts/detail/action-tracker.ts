import { onUnmounted, ref } from "vue";
import { getOperatorAction, getOrder, type OperatorAction, type Order } from "@/api/trade";
import { createLatestRequestGuard } from "@/utils/latest-request";

const ORDER_ACTIONS = ["SUBMIT_ORDER", "MANUAL_ORDER"];
const ORDER_FINAL_STATES = ["FILLED", "CANCELED", "PARTIALLY_CANCELED", "REJECTED", "EXPIRED"];

export interface ActionTrackerOptions {
  /** 当前页面对应的组合账户编号；切换后旧轮询的结果不再生效 */
  logicalAccountId: () => string | undefined;
  /** 操作的状态变了（开始、完成），页面要刷新账户 */
  onChanged: () => void;
}

/**
 * 跟踪最近一次人工操作（清仓、下单、撤单）的进度：每 1.5 秒按操作编号查一次，
 * 下单类操作还要继续查订单直到订单结束。查询失败只记下提示，下一轮重试；查询成功后提示清除。
 */
export function useActionTracker(options: ActionTrackerOptions) {
  const action = ref<OperatorAction | null>(null);
  const submittedOrder = ref<Order | null>(null);
  const error = ref("");
  const polling = createLatestRequestGuard();
  let timer: ReturnType<typeof setInterval> | null = null;

  function stop() {
    polling.invalidate();
    if (timer) clearInterval(timer);
    timer = null;
  }

  /** onObserved：每次成功查到操作后调用，下单弹窗用它确认“结果不明”的请求已经落地。 */
  function start(actionId: string, onObserved?: (action: OperatorAction) => void) {
    stop();
    const request = polling.begin();
    const logicalAccountId = options.logicalAccountId();
    let busy = false;
    timer = setInterval(async () => {
      if (busy) return;
      busy = true;
      try {
        // 每 1.5 秒一次的后台查询：失败显示在跟踪器自己的错误里，不弹全局提示
        const rsp = await getOperatorAction(actionId, { silent: true });
        if (!request.isLatest() || options.logicalAccountId() !== logicalAccountId) return;
        const isOrderAction = ORDER_ACTIONS.includes(rsp.action.action_type);
        if (action.value?.action_id !== rsp.action.action_id || !isOrderAction) submittedOrder.value = null;
        const changed = action.value?.action_id !== rsp.action.action_id || action.value?.status !== rsp.action.status;
        error.value = "";
        action.value = rsp.action;
        if (changed) options.onChanged();
        onObserved?.(rsp.action);
        let orderId = submittedOrder.value?.order_id;
        if (rsp.action.result_json && isOrderAction) {
          const progress = JSON.parse(rsp.action.result_json) as { order_id?: string };
          orderId = progress.order_id || orderId;
        }
        if (orderId) {
          const orderRsp = await getOrder(orderId, { silent: true });
          if (!request.isLatest() || options.logicalAccountId() !== logicalAccountId) return;
          submittedOrder.value = orderRsp.order;
        }
        const orderDone = !orderId || ORDER_FINAL_STATES.includes(submittedOrder.value?.state || "");
        if (rsp.action.status !== "RUNNING" && orderDone) stop();
      } catch (cause) {
        if (request.isLatest()) error.value = cause instanceof Error ? cause.message : "查询暂不可用";
      } finally {
        busy = false;
      }
    }, 1500);
  }

  function reset() {
    stop();
    action.value = null;
    submittedOrder.value = null;
    error.value = "";
  }

  onUnmounted(stop);
  return { action, submittedOrder, error, start, stop, reset };
}

export type ActionTracker = ReturnType<typeof useActionTracker>;
