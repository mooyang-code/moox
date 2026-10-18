interface RouteLike {
  path: string;
  meta?: { reuseOnQuery?: boolean } | Record<string, unknown>;
}

/** 是否显示全局“页面切换中”：同一路径、声明了 reuseOnQuery 的 query 导航不显示，否则会卸载页面内容。 */
export function shouldShowRouteLoading(to: RouteLike, from: RouteLike | undefined): boolean {
  return !(from && to.path === from.path && to.meta?.reuseOnQuery === true);
}
