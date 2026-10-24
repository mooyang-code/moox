/* ts命名空间 */
/* 路由-Menu */
declare namespace Menu {
  interface MenuOptions {
    id: string;
    parentId: string;
    path: string;
    name: string;
    redirect?: string;
    component?: string | (() => Promise<unknown>);
    meta: MetaType;
    children?: MenuOptions[] | null | any;
  }
  interface MetaType {
    title: string;
    hide: boolean;
    disable: boolean;
    keepAlive: boolean;
    affix: boolean;
    link?: string;
    iframe?: boolean;
    icon?: string;
    svgIcon?: string;
    sort?: number;
    type?: number;
    /** 隐藏路由进入时侧栏高亮的菜单 name */
    activeMenu?: string;
    /** 同一路径仅 query 变化时复用页面组件（不重建、不闪全局加载态），页面自己监听 query。 */
    reuseOnQuery?: boolean;
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
  VITE_ADMIN_ORIGIN?: string;
}
