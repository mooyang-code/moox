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

interface MenuTreeLike {
  name: string;
  meta?: { type?: number; hide?: boolean } | Record<string, unknown>;
  children?: MenuTreeLike[] | null;
}

/**
 * 选中项在菜单树里的全部父菜单（由外到内）：子菜单页面打开时据此把父菜单逐级展开。
 * 父菜单的 key 是它的路由 name（见 menu-item.vue）；选中项不在这棵树里时返回空。
 */
export function resolveOpenKeys(tree: MenuTreeLike[] | null | undefined, selectedKeys: string[]): string[] {
  const open: string[] = [];
  for (const key of selectedKeys) {
    const path = parentPath(tree || [], key);
    for (const name of path || []) {
      if (!open.includes(name)) open.push(name);
    }
  }
  return open;
}

function parentPath(tree: MenuTreeLike[], key: string): string[] | undefined {
  for (const item of tree) {
    if (item.name === key) return [];
    if (!item.children?.length) continue;
    const below = parentPath(item.children, key);
    if (below) return [item.name, ...below];
  }
  return undefined;
}

/** 侧栏菜单的一项：有 children 的是目录，没有的是页面。 */
export interface MenuNode {
  name: string;
  path: string;
  title: string;
  svgIcon: string;
  children: MenuNode[];
}

interface RouteTreeLike {
  name: string;
  path: string;
  meta: Record<string, unknown>;
  children?: RouteTreeLike[];
}

/** 由路由表生成侧栏菜单：顺序就是路由表的顺序，隐藏路由（meta.hide）不进菜单。 */
export function buildMenuTree(routes: readonly RouteTreeLike[]): MenuNode[] {
  return routes
    .filter(route => !route.meta.hide)
    .map(route => ({
      name: route.name,
      path: route.path,
      title: String(route.meta.title ?? ""),
      svgIcon: String(route.meta.svgIcon ?? ""),
      children: buildMenuTree(route.children ?? [])
    }));
}
