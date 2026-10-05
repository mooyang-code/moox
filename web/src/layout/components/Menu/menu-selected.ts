interface SelectedRouteLike {
  name?: string | symbol | null;
  meta?: { activeMenu?: string } | Record<string, unknown>;
}

/** 侧栏 / 顶栏菜单的选中项：隐藏路由用 meta.activeMenu 指向所属菜单，否则取路由 name。直接读路由，不走 store。 */
export function resolveSelectedKeys(route: SelectedRouteLike): string[] {
  const activeMenu = route.meta?.activeMenu;
  if (typeof activeMenu === "string" && activeMenu) return [activeMenu];
  return typeof route.name === "string" && route.name ? [route.name] : [];
}
