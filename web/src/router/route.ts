import { HOME_PATH } from "@/config/index";

/**
 * 管理台唯一的路由表：侧栏菜单、标签页、交易三环境的菜单都从这里读。
 * - 一级菜单是 layoutRoutes 的顺序；有 children 的是目录（标题、图标写在 meta），没有的是单页菜单。
 * - meta.hide 的路由不进菜单与标签页（编辑页、详情页），用 meta.activeMenu 指向侧栏高亮的菜单。
 * - query 变化时复用页面组件（见 route-view-key.ts）；同一页面有几条地址时写相同的 meta.viewKey。
 * 本文件只放数据，页面都是按需加载，别的模块可以直接引用它而不带进页面代码。
 */

// 组合详情的几条地址（各环境一条，加只带组合编号的通用地址）是同一个页面：在它们之间换地址时复用组件
const DETAIL_VIEW_KEY = "trading-account-detail";

// 一个环境的账户列表菜单与它的组合详情（隐藏路由，侧栏高亮所属菜单）；三个环境共用同一个列表页，meta.env 写明环境。
const tradingAccountRoutes = (name: string, path: string, title: string, env: string) => [
  {
    path,
    name,
    component: () => import("@/views/trading/accounts/index.vue"),
    meta: { title, env }
  },
  {
    path: `${path}/:logical_account_id`,
    name: `${name}-detail`,
    component: () => import("@/views/trading/accounts/detail/index.vue"),
    meta: { title: "交易账户详情", hide: true, activeMenu: name, viewKey: DETAIL_VIEW_KEY, env }
  }
];

export const layoutRoutes = [
  {
    path: HOME_PATH,
    name: "home",
    component: () => import("@/views/home/home.vue"),
    meta: { title: "首页", svgIcon: "home", affix: true }
  },
  {
    path: "/collector",
    name: "compute-collector",
    redirect: "/collector/tasks",
    meta: { title: "数据采集", svgIcon: "functions" },
    children: [
      {
        path: "/collector/tasks",
        name: "collector-tasks",
        component: () => import("@/views/collector/task-management/index.vue"),
        meta: { title: "采集任务" }
      },
      {
        path: "/data/sources",
        name: "data-sources",
        component: () => import("@/views/data/sources/index.vue"),
        meta: { title: "数据源" }
      },
      {
        path: "/data/subjects",
        name: "data-subjects",
        component: () => import("@/views/data/subjects/index.vue"),
        meta: { title: "数据对象" }
      },
      {
        path: "/data/fields",
        name: "data-fields",
        component: () => import("@/views/data/fields/index.vue"),
        meta: { title: "基础字段" }
      }
    ]
  },
  {
    path: "/factor",
    name: "factor-compute",
    redirect: "/factor/overview",
    meta: { title: "因子计算", svgIcon: "experiment" },
    children: [
      {
        path: "/factor/overview",
        name: "factor-overview",
        component: () => import("@/views/factor/overview/index.vue"),
        meta: { title: "因子总览" }
      },
      {
        path: "/factor/definitions",
        name: "factor-definitions",
        component: () => import("@/views/factor/definitions/index.vue"),
        meta: { title: "因子定义" }
      },
      {
        path: "/factor/tasks",
        name: "factor-tasks",
        component: () => import("@/views/factor/task-management/index.vue"),
        meta: { title: "计算任务" }
      },
      {
        path: "/factor/definitions/new",
        name: "factor-definition-new",
        component: () => import("@/views/factor/editor/index.vue"),
        meta: { title: "新建因子", hide: true, activeMenu: "factor-definitions" }
      },
      {
        path: "/factor/definitions/:factorId/edit",
        name: "factor-definition-edit",
        component: () => import("@/views/factor/editor/index.vue"),
        meta: { title: "编辑因子", hide: true, activeMenu: "factor-definitions" }
      }
    ]
  },
  {
    path: "/strategy",
    name: "strategy",
    redirect: "/strategy/overview",
    meta: { title: "策略管理", svgIcon: "mind-mapping" },
    children: [
      {
        path: "/strategy/overview",
        name: "strategy-overview",
        component: () => import("@/views/strategy/overview/index.vue"),
        meta: { title: "策略总览" }
      },
      {
        path: "/strategy/definitions",
        name: "strategy-definitions",
        component: () => import("@/views/strategy/definitions/index.vue"),
        meta: { title: "策略定义" }
      },
      {
        path: "/strategy/tasks",
        name: "strategy-tasks",
        component: () => import("@/views/strategy/workbench/index.vue"),
        meta: { title: "策略运行" }
      },
      {
        path: "/strategy/definitions/new",
        name: "strategy-definition-new",
        component: () => import("@/views/strategy/editor/index.vue"),
        meta: { title: "新建策略", hide: true, activeMenu: "strategy-definitions" }
      },
      {
        path: "/strategy/definitions/:strategyId/edit",
        name: "strategy-definition-edit",
        component: () => import("@/views/strategy/editor/index.vue"),
        meta: { title: "编辑策略", hide: true, activeMenu: "strategy-definitions" }
      },
      {
        path: "/strategy/detail/:instanceId",
        name: "strategy-detail",
        component: () => import("@/views/strategy/detail/index.vue"),
        meta: { title: "实例详情", hide: true, activeMenu: "strategy-tasks" }
      }
    ]
  },
  {
    path: "/trading",
    name: "trading",
    redirect: "/trading/paper",
    meta: { title: "交易管理", svgIcon: "balance-inquiry" },
    children: [
      ...tradingAccountRoutes("trading-paper", "/trading/paper", "模拟账户", "paper"),
      ...tradingAccountRoutes("trading-testnet", "/trading/testnet", "测试账户", "testnet"),
      ...tradingAccountRoutes("trading-live", "/trading/live", "实盘账户", "prod"),
      // 下架名单是全局的，不分环境和空间，单独一个菜单。
      {
        path: "/trading/blocked",
        name: "trading-blocked",
        component: () => import("@/views/trading/blocked/index.vue"),
        meta: { title: "下架名单" }
      },
      // 只知道组合编号、不知道环境的链接（策略页、事件中心）走这个地址；详情页读到组合后换到所属环境的地址。
      {
        path: "/trading/accounts/:logical_account_id",
        name: "trading-account-detail",
        component: () => import("@/views/trading/accounts/detail/index.vue"),
        meta: { title: "交易账户详情", hide: true, activeMenu: "trading-live", viewKey: DETAIL_VIEW_KEY }
      }
    ]
  },
  {
    path: "/archive",
    name: "archive",
    redirect: "/archive/tasks",
    meta: { title: "数据归档", svgIcon: "folder-menu" },
    children: [
      {
        path: "/archive/tasks",
        name: "archive-tasks",
        component: () => import("@/views/archive/tasks/index.vue"),
        meta: { title: "归档任务" }
      },
      {
        path: "/archive/storage",
        name: "archive-storage",
        component: () => import("@/views/archive/storage/index.vue"),
        meta: { title: "归档存储" }
      }
    ]
  },
  // 事件中心只有一页，页签（事件 / 订阅 / 执行记录）在 query 里。
  {
    path: "/events",
    name: "events",
    component: () => import("@/views/events/index.vue"),
    meta: { title: "事件中心", svgIcon: "bell" }
  },
  {
    path: "/ops",
    name: "ops",
    redirect: "/ops/monitor",
    meta: { title: "资源与运维", svgIcon: "defend" },
    children: [
      {
        path: "/ops/monitor",
        name: "ops-monitor",
        component: () => import("@/views/ops/monitor/index.vue"),
        meta: { title: "监控告警" }
      },
      {
        path: "/ops/deployments",
        name: "ops-deployments",
        component: () => import("@/views/ops/deployments/index.vue"),
        meta: { title: "服务管理" }
      },
      {
        path: "/ops/hosts",
        name: "ops-hosts",
        component: () => import("@/views/ops/host-workbench/index.vue"),
        meta: { title: "主机工作台" }
      },
      // 活动日志与重要日志是同一页的两个页签（?tab=）。
      {
        path: "/ops/logs",
        name: "ops-logs",
        component: () => import("@/views/ops/log-center/index.vue"),
        meta: { title: "日志中心" }
      }
    ]
  },
  {
    path: "/settings",
    name: "settings",
    redirect: "/settings/spaces",
    meta: { title: "系统设置", svgIcon: "set" },
    children: [
      {
        path: "/settings/spaces",
        name: "settings-spaces",
        component: () => import("@/views/settings/spaces/index.vue"),
        meta: { title: "空间管理" }
      },
      {
        path: "/settings/secrets",
        name: "settings-secrets",
        component: () => import("@/views/settings/secrets/index.vue"),
        meta: { title: "密钥管理" }
      }
    ]
  }
];

export const staticRoutes = [
  {
    path: "/",
    redirect: HOME_PATH
  },
  {
    path: "/login",
    name: "login",
    component: () => import("@/views/login/login.vue"),
    meta: { title: "登录" }
  },
  {
    path: "/layout",
    name: "layout",
    redirect: HOME_PATH,
    component: () => import("@/layout/index.vue"),
    children: layoutRoutes
  }
];

// 兜底路由：没有匹配的路径都落到 404 页
export const notFoundRoutes = [
  {
    path: "/:path(.*)*",
    name: "not-found",
    component: () => import("@/views/error/404.vue"),
    meta: { title: "未找到页面", hide: true }
  }
];

/** 路由表里的一条路由（只取菜单、标签页用到的部分）。 */
export interface RouteEntry {
  path: string;
  name: string;
  meta: Record<string, unknown>;
  children?: RouteEntry[];
}

/** 路由表展平成一维（目录在前、子路由在后）。 */
export function flatRoutes(routes: readonly RouteEntry[] = layoutRoutes as RouteEntry[]): RouteEntry[] {
  return routes.flatMap(route => [route, ...flatRoutes(route.children ?? [])]);
}
