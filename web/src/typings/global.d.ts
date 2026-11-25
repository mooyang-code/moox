/* ts命名空间 */
/* 路由-Menu */
declare namespace Menu {
  /** 标签栏里的一条路由（由当前路由深拷贝而来） */
  interface MenuOptions {
    path: string;
    name: string;
    meta: MetaType;
    query?: Record<string, unknown>;
    params?: Record<string, unknown>;
  }
  /** 路由表（router/route.ts）里 meta 的字段 */
  interface MetaType {
    title: string;
    /** 不进菜单与标签栏（编辑页、详情页） */
    hide?: boolean;
    /** 标签页不能关闭（首页） */
    affix?: boolean;
    /** 一级菜单的图标 */
    svgIcon?: string;
    /** 隐藏路由进入时侧栏高亮的菜单 name */
    activeMenu?: string;
    /** 同一个页面有几条地址时写相同的 viewKey：在这些地址之间换时复用页面组件 */
    viewKey?: string;
    /** 交易账户页面所属的环境：paper、testnet、prod */
    env?: string;
  }
}
/* tabs菜单 */
declare namespace TabsMenu {
  interface list {
    list: Array<{ type: number; icon: string; title: string }>;
  }
  interface menu {
    type: number;
    icon: string;
    title: string;
  }
}

/* viteEnv */
interface ViteEnv {
  VITE_GLOB_APP_TITLE: string;
  VITE_ADMIN_PROXY_TARGET?: string;
}
