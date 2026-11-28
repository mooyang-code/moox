import { computed, type ComputedRef } from "vue";
import { useRoute, useRouter, type LocationQuery, type LocationQueryRaw } from "vue-router";

export interface UseQueryTabOptions<T extends string> {
  /** 允许的页签键列表；未指定 defaultTab 时第一项为默认页签。 */
  tabs: readonly T[];
  /** 默认页签；默认取 tabs[0]。 */
  defaultTab?: T;
  /** URL query 参数名，默认 "tab"。 */
  param?: string;
  /** 写入路由的目标（按命名路由或路径）；省略时使用当前 route.path。 */
  target?: { name: string } | { path: string };
  /** 默认页签是否省略 URL 参数，默认 true。 */
  omitDefault?: boolean;
  /** 切换页签时如何构造完整 query；省略时只写 tab 参数并清空其他参数。 */
  buildQuery?: (tab: T, currentQuery: LocationQuery) => LocationQueryRaw;
}

export interface UseQueryTabReturn<T extends string> {
  /** 当前页签：完全由 URL 派生，避免本地状态与地址漂移。 */
  activeTab: ComputedRef<T>;
  /** 把外部输入归一化到合法页签键，非法值回落到默认页签。 */
  normalizeTab: (value: unknown) => T;
  /** 切换页签并通过 router.replace 写回 URL。 */
  setTab: (value: string | number) => void;
}

/**
 * 把标题页签状态同步到 URL query 参数（默认 ?tab=）：
 * 当前页签始终只从 route.query 派生，点击切换只写 URL，避免新页签在路由生效前被旧 query 覆盖。
 */
export function useQueryTab<T extends string>(options: UseQueryTabOptions<T>): UseQueryTabReturn<T> {
  const route = useRoute();
  const router = useRouter();
  const param = options.param ?? "tab";
  const fallback = options.defaultTab ?? options.tabs[0];
  const omitDefault = options.omitDefault ?? true;
  const allowed = new Set<string>(options.tabs);

  function normalizeTab(value: unknown): T {
    const raw = Array.isArray(value) ? value[0] : value;
    return typeof raw === "string" && allowed.has(raw) ? (raw as T) : fallback;
  }

  const activeTab = computed<T>(() => normalizeTab(route.query[param]));

  function setTab(value: string | number) {
    const tab = normalizeTab(value);
    const query: LocationQueryRaw = options.buildQuery
      ? options.buildQuery(tab, route.query)
      : omitDefault && tab === fallback
        ? {}
        : { [param]: tab };
    const destination = options.target ? { ...options.target, query } : { path: route.path, query };
    void router.replace(destination);
  }

  return { activeTab, normalizeTab, setTab };
}
