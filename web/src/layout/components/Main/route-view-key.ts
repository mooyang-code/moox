interface RouteViewLike {
  path: string;
  fullPath: string;
  meta?: { reuseOnQuery?: boolean } | Record<string, unknown>;
}

/** 路由视图的组件 key：声明了 reuseOnQuery 的页面只按 path 区分，query 变化时复用组件；其余页面按完整路径重建。 */
export function routeViewKey(route: RouteViewLike): string {
  return route.meta?.reuseOnQuery === true ? route.path : route.fullPath;
}
