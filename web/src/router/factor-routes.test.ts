import { describe, expect, it } from "vitest";
import { systemMenu } from "@/api/modules/system/static-menu";
import { routeViewKey } from "@/layout/components/Main/route-view-key";
import { staticRoutes } from "./route";

type RouteLike = { path: string; meta?: Record<string, unknown>; children?: RouteLike[] | null };

function findRoute(routes: RouteLike[], path: string): RouteLike | undefined {
  for (const route of routes) {
    if (route.path === path) return route;
    const child = route.children && findRoute(route.children, path);
    if (child) return child;
  }
  return undefined;
}

const reusePaths = ["/factor/definitions", "/factor/tasks", "/strategy/definitions", "/strategy/tasks"];

describe("query 变化时复用的页面", () => {
  // 定义页的 ?category=、结果页的 ?fc= 和 ?task= 改写时不能重建页面，否则未进 URL 的筛选会丢失。
  // 线上实际注册的是菜单路由（会覆盖 route.ts 的同名声明），所以两处都要声明。
  it.each(reusePaths)("%s 在菜单与静态路由里都声明了 reuseOnQuery", path => {
    const menuEntry = (systemMenu as RouteLike[]).find(item => item.path === path);
    const staticEntry = findRoute(staticRoutes as unknown as RouteLike[], path);
    expect(menuEntry?.meta?.reuseOnQuery).toBe(true);
    expect(staticEntry?.meta?.reuseOnQuery).toBe(true);
    expect(routeViewKey({ path, fullPath: `${path}?a=1`, meta: menuEntry?.meta })).toBe(path);
  });
});
