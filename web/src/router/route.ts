import { HOME_PATH } from "@/config/index";
import Layout from "@/layout/index.vue";

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
      {
        path: "/trading/accounts",
        name: "trading-accounts",
        component: () => import("@/views/trading/accounts/index.vue"),
        meta: { title: "trading-accounts", reuseOnQuery: true }
      },
      {
        path: "/trading/accounts/:logical_account_id",
        name: "trading-account-detail",
        component: () => import("@/views/trading/accounts/detail/index.vue"),
        meta: { title: "trading-account-detail", hide: true, activeMenu: "trading-accounts", reuseOnQuery: true }
      },
      {
        path: "/ops/monitor",
        name: "ops-monitor",
        component: () => import("@/views/ops/monitor/index.vue"),
        meta: { title: "ops-monitor", reuseOnQuery: true }
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
