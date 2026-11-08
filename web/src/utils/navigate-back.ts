import type { RouteLocationRaw, Router } from "vue-router";

/**
 * 返回原处：有站内来源（vue-router 在 history.state.back 里记着上一页）时后退，回到来的那一页和它的筛选；
 * 直接打开的（新标签页、刷新后的书签）没有来源，回到 fallback 指定的列表。
 */
export function goBackOr(router: Router, fallback: RouteLocationRaw) {
  const state = window.history.state as { back?: unknown } | null;
  if (typeof state?.back === "string" && state.back) router.back();
  else void router.push(fallback);
}
