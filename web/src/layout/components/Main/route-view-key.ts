interface RouteViewLike {
  path: string;
  params?: Record<string, unknown>;
  query?: Record<string, unknown>;
  meta?: { viewKey?: string; scope?: string; spaceRedirect?: string } | Record<string, unknown>;
}

/**
 * 路由视图的组件 key：
 * - 声明了 viewKey 的几条路由是同一个页面的不同地址（例如组合详情按环境有几条地址），key 取 viewKey 加路由参数，
 *   在这几条地址之间换时复用组件，不重建；
 * - 其余页面按 path 区分：只有 query 变化时复用组件（不重建、不闪全局加载态），页面自己监听 query；
 * - meta.scope === "space" 的空间内页面附带空间代次，切换空间时整页重建；全局页面不带代次，切空间不打断。
 */
export function routeViewKey(route: RouteViewLike, spaceGeneration = 0): string {
  const viewKey = route.meta?.viewKey;
  const base = typeof viewKey === "string" && viewKey ? [viewKey, ...Object.values(route.params ?? {})].join("/") : route.path;
  return route.meta?.scope === "space" ? `${base}@space:${spaceGeneration}` : base;
}

/**
 * 真正切换空间（A → B）时，空间内页面需要做的路由清理：
 * - 带对象 ID 的详情页（声明了 meta.spaceRedirect）：对象不属于新空间，跳回对应列表；
 * - 其余带 URL 查询参数的空间内页面：保留 `tab` 页签参数，清掉旧空间的筛选、选中与分页参数；
 * - 全局页面、没有 URL 参数或仅带有效 `tab` 的页面无需导航。
 */
export function spaceSwitchNavigation(route: RouteViewLike): { path: string; query?: Record<string, string> } | null {
  if (route.meta?.scope !== "space") return null;
  const redirect = route.meta?.spaceRedirect;
  if (typeof redirect === "string" && redirect) return { path: redirect };
  const query = route.query;
  if (!query) return null;
  const keys = Object.keys(query);
  if (keys.length === 0) return null;
  const rawTab = query.tab;
  const firstTab = Array.isArray(rawTab) ? rawTab[0] : rawTab;
  const keptTab = typeof firstTab === "string" && firstTab ? firstTab : undefined;
  if (keys.length === 1 && keys[0] === "tab" && keptTab !== undefined) return null;
  return { path: route.path, query: keptTab !== undefined ? { tab: keptTab } : {} };
}
