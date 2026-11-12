import { HOME_PATH } from "@/config/index";
import Layout from "@/layout/index.vue";
import { environmentMenus, type Environment } from "@/components/trade-confirm/environment";

// 组合详情的几条地址（各环境一条，加只带组合编号的通用地址）是同一个页面：在它们之间换地址时复用组件
const DETAIL_VIEW_KEY = "trading-account-detail";

// 三个环境各一个账户列表菜单，加上各自的组合详情（隐藏路由，侧栏高亮所属菜单）。
const tradingAccountRoutes = (Object.keys(environmentMenus) as Environment[]).flatMap(env => {
  const menu = environmentMenus[env];
  return [
    {
      path: menu.path,
      name: menu.name,
      component: () => import("@/views/trading/accounts/index.vue"),
      meta: { title: menu.name, reuseOnQuery: true, env }
    },
    {
      path: `${menu.path}/:logical_account_id`,
      name: `${menu.name}-detail`,
      component: () => import("@/views/trading/accounts/detail/index.vue"),
      meta: {
        title: "trading-account-detail",
        hide: true,
        activeMenu: menu.name,
        reuseOnQuery: true,
        viewKey: DETAIL_VIEW_KEY,
        env
      }
    }
  ];
});

export const staticRoutes = [
  {
    path: "/",
    redirect: HOME_PATH
  },
  {
    path: "/login",
    name: "login",
    component: () => import("@/views/login/login.vue"),
    meta: { title: "login" }
  },
  {
    path: "/layout",
    name: "layout",
    redirect: HOME_PATH,
    component: Layout,
    children: [
      {
        path: "/home",
        name: "home",
        component: () => import("@/views/home/home.vue"),
        meta: { title: "home" }
      },
      {
        path: "/settings/spaces",
        name: "settings-spaces",
        component: () => import("@/views/settings/spaces/index.vue"),
        meta: { title: "settings-spaces" }
      },
      {
        path: "/settings/secrets",
        name: "settings-secrets",
        component: () => import("@/views/settings/secrets/index.vue"),
        meta: { title: "settings-secrets" }
      },
      {
        path: "/events",
        name: "events",
        component: () => import("@/views/events/index.vue"),
        meta: { title: "events", reuseOnQuery: true }
      },
      {
        path: "/data/sources",
        name: "data-sources",
        component: () => import("@/views/data/sources/index.vue"),
        meta: { title: "data-sources" }
      },
      {
        path: "/data/subjects",
        name: "data-subjects",
        component: () => import("@/views/data/subjects/index.vue"),
        meta: { title: "data-subjects", reuseOnQuery: true }
      },
      {
        path: "/data/fields",
        name: "data-fields",
        component: () => import("@/views/data/fields/index.vue"),
        meta: { title: "data-fields", reuseOnQuery: true }
      },
      {
        path: "/archive/tasks",
        name: "archive-tasks",
        component: () => import("@/views/archive/tasks/index.vue"),
        meta: { title: "archive-tasks", reuseOnQuery: true }
      },
      {
        path: "/archive/storage",
        name: "archive-storage",
        component: () => import("@/views/archive/storage/index.vue"),
        meta: { title: "archive-storage" }
      },
      {
        path: "/factor/overview",
        name: "factor-overview",
        component: () => import("@/views/factor/overview/index.vue"),
        meta: { title: "factor-overview" }
      },
      {
        path: "/factor/definitions",
        name: "factor-definitions",
        component: () => import("@/views/factor/definitions/index.vue"),
        meta: { title: "factor-definitions", reuseOnQuery: true }
      },
      {
        path: "/factor/tasks",
        name: "factor-tasks",
        component: () => import("@/views/factor/task-management/index.vue"),
        meta: { title: "factor-tasks", reuseOnQuery: true }
      },
      {
        path: "/factor/definitions/new",
        name: "factor-definition-new",
        component: () => import("@/views/factor/editor/index.vue"),
        meta: { title: "factor-definition-new", hide: true, activeMenu: "factor-definitions" }
      },
      {
        path: "/factor/definitions/:factorId/edit",
        name: "factor-definition-edit",
        component: () => import("@/views/factor/editor/index.vue"),
        meta: { title: "factor-definition-edit", hide: true, activeMenu: "factor-definitions" }
      },
      {
        path: "/strategy/overview",
        name: "strategy-overview",
        component: () => import("@/views/strategy/overview/index.vue"),
        meta: { title: "strategy-overview" }
      },
      {
        path: "/strategy/definitions",
        name: "strategy-definitions",
        component: () => import("@/views/strategy/definitions/index.vue"),
        meta: { title: "strategy-definitions", reuseOnQuery: true }
      },
      {
        path: "/strategy/tasks",
        name: "strategy-tasks",
        component: () => import("@/views/strategy/workbench/index.vue"),
        meta: { title: "strategy-tasks", reuseOnQuery: true }
      },
      {
        path: "/strategy/definitions/new",
        name: "strategy-definition-new",
        component: () => import("@/views/strategy/editor/index.vue"),
        meta: { title: "strategy-definition-new", hide: true, activeMenu: "strategy-definitions" }
      },
      {
        path: "/strategy/definitions/:strategyId/edit",
        name: "strategy-definition-edit",
        component: () => import("@/views/strategy/editor/index.vue"),
        meta: { title: "strategy-definition-edit", hide: true, activeMenu: "strategy-definitions" }
      },
      {
        path: "/strategy/detail/:instanceId",
        name: "strategy-detail",
        component: () => import("@/views/strategy/detail/index.vue"),
        meta: { title: "strategy-detail", hide: true, activeMenu: "strategy-tasks" }
      },
      {
        path: "/ops/activity",
        name: "ops-activity",
        component: () => import("@/views/ops/activity/index.vue"),
        meta: { title: "ops-activity" }
      },
      {
        path: "/collector/tasks",
        name: "collector-tasks",
        component: () => import("@/views/collector/task-management/index.vue"),
        meta: { title: "collector-tasks", reuseOnQuery: true }
      },
      ...tradingAccountRoutes,
      {
        path: "/trading/blocked",
        name: "trading-blocked",
        component: () => import("@/views/trading/blocked/index.vue"),
        meta: { title: "trading-blocked" }
      },
      // 只知道组合编号、不知道环境的链接（策略页、事件中心）走这个地址；详情页读到组合后换到所属环境的地址。
      {
        path: "/trading/accounts/:logical_account_id",
        name: "trading-account-detail",
        component: () => import("@/views/trading/accounts/detail/index.vue"),
        meta: {
          title: "trading-account-detail",
          hide: true,
          activeMenu: environmentMenus.prod.name,
          reuseOnQuery: true,
          viewKey: DETAIL_VIEW_KEY
        }
      },
      {
        path: "/ops/monitor",
        name: "ops-monitor",
        component: () => import("@/views/ops/monitor/index.vue"),
        meta: { title: "ops-monitor", reuseOnQuery: true }
      },
      {
        path: "/ops/logs",
        name: "ops-logs",
        component: () => import("@/views/ops/log-records/index.vue"),
        meta: { title: "ops-logs" }
      },
      {
        path: "/ops/deployments",
        name: "ops-deployments",
        component: () => import("@/views/ops/deployments/index.vue"),
        meta: { title: "ops-deployments", reuseOnQuery: true }
      },
      {
        path: "/ops/hosts",
        name: "ops-hosts",
        component: () => import("@/views/ops/host-workbench/index.vue"),
        meta: { title: "ops-hosts", reuseOnQuery: true }
      },
      {
        path: "/ops/storage/nodes",
        name: "ops-storage",
        component: () => import("@/views/ops/storage/nodes.vue"),
        meta: { title: "ops-storage" }
      }
    ]
  }
];

// 兜底路由：没有匹配的路径都落到 404 页
export const notFoundRoutes = [
  {
    path: "/:path(.*)*",
    name: "not-found",
    component: () => import("@/views/error/404.vue"),
    meta: { title: "not-found", hide: true }
  }
];
