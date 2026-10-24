import { describe, expect, it } from "vitest";
import { staticRoutes } from "./route";
import { routeViewKey } from "@/layout/components/Main/route-view-key";

type RouteLike = { path: string; meta?: Record<string, unknown>; children?: RouteLike[] };

function findRoute(routes: RouteLike[], path: string): RouteLike | undefined {
  for (const route of routes) {
    if (route.path === path) return route;
    const child = route.children && findRoute(route.children, path);
    if (child) return child;
  }
  return undefined;
}

describe("因子页面路由", () => {
  // 定义页的 ?category=、结果页的 ?fc= 和 ?set= 改写时不能重建页面，否则未进 URL 的筛选会丢失。
  it.each(["/factor/definitions", "/factor/tasks"])("%s 在 query 变化时复用组件", path => {
    const route = findRoute(staticRoutes as unknown as RouteLike[], path);
    expect(route?.meta?.reuseOnQuery).toBe(true);
    expect(routeViewKey({ path, fullPath: `${path}?a=1`, meta: route?.meta })).toBe(path);
  });
});
