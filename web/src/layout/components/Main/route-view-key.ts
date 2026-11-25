interface RouteViewLike {
  path: string;
  params?: Record<string, unknown>;
  meta?: { viewKey?: string } | Record<string, unknown>;
}

/**
 * 路由视图的组件 key：
 * - 声明了 viewKey 的几条路由是同一个页面的不同地址（例如组合详情按环境有几条地址），key 取 viewKey 加路由参数，
 *   在这几条地址之间换时复用组件，不重建；
 * - 其余页面按 path 区分：只有 query 变化时复用组件（不重建、不闪全局加载态），页面自己监听 query。
 */
export function routeViewKey(route: RouteViewLike): string {
  const viewKey = route.meta?.viewKey;
  if (typeof viewKey === "string" && viewKey) return [viewKey, ...Object.values(route.params ?? {})].join("/");
  return route.path;
}
