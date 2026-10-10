import { HOME_PATH } from "@/config/index";
import Layout from "@/layout/index.vue";

type RedirectLocation = { query: Record<string, unknown> };

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
        path: "/data/sources",
        name: "data-sources",
        component: () => import("@/views/data/sources/index.vue"),
        meta: { title: "data-sources" }
      },
      {
        path: "/data/subjects",
        name: "data-subjects",
        component: () => import("@/views/data/subjects/index.vue"),
        meta: { title: "data-subjects" }
      },
      {
        path: "/data/fields",
        name: "data-fields",
        component: () => import("@/views/data/fields/index.vue"),
        meta: { title: "data-fields" }
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
        meta: { title: "factor-definitions" }
      },
      {
        path: "/factor/tasks",
        name: "factor-tasks",
        component: () => import("@/views/factor/task-management/index.vue"),
        meta: { title: "factor-tasks" }
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
        meta: { title: "strategy-definitions" }
      },
      {
        path: "/strategy/running",
        name: "strategy-running",
        component: () => import("@/views/strategy/running/index.vue"),
        meta: { title: "strategy-running" }
      },
      {
        path: "/strategy/definitions/new",
        name: "strategy-definition-new",
        component: () => import("@/views/strategy/editor/index.vue"),
        meta: { title: "strategy-definition-new", hide: true }
      },
      {
        path: "/strategy/definitions/:strategyId/edit",
        name: "strategy-definition-edit",
        component: () => import("@/views/strategy/editor/index.vue"),
        meta: { title: "strategy-definition-edit", hide: true }
      },
      {
        path: "/strategy/detail/:instanceId",
        name: "strategy-detail",
        component: () => import("@/views/strategy/detail/index.vue"),
        meta: { title: "strategy-detail", hide: true }
      },
      {
        path: "/strategy/replay",
        name: "strategy-replay",
        component: () => import("@/views/strategy/replay/index.vue"),
        meta: { title: "strategy-replay" }
      },
      {
        path: "/data/import",
        name: "data-import",
        component: () => import("@/views/data/import/index.vue"),
        meta: { title: "data-import" }
      },
      {
        path: "/collector/cloudnodes",
        name: "collector-cloudnodes",
        redirect: (to: RedirectLocation) => ({
          path: "/collector/tasks",
          query: { ...to.query, tab: "executors" }
        }),
        meta: { title: "collector-cloudnodes" }
      },
      {
        path: "/collector/tasks",
        name: "collector-tasks",
        component: () => import("@/views/collector/task-management/index.vue"),
        meta: { title: "collector-tasks" }
      },
      {
        path: "/trading/accounts",
        name: "trading-accounts",
        component: () => import("@/views/trading/account-workbench/index.vue"),
        meta: { title: "trading-accounts" }
      },
      {
        path: "/trading/logical-accounts",
        name: "trading-logical-accounts",
        redirect: (to: RedirectLocation) => ({
          path: "/trading/accounts",
          query: { ...to.query, mode: undefined, view: "strategy" }
        }),
        meta: { title: "trading-logical-accounts" }
      },
      {
        path: "/trading/positions",
        name: "trading-positions",
        component: () => import("@/views/trading/position-detail/position-detail.vue"),
        meta: { title: "trading-positions" }
      },
      {
        path: "/trading/orders",
        name: "trading-orders",
        component: () => import("@/views/trading/trade-record/trade-record.vue"),
        meta: { title: "trading-orders" }
      },
      {
        path: "/ops/monitor",
        name: "ops-monitor",
        component: () => import("@/views/ops/monitor/index.vue"),
        meta: { title: "ops-monitor" }
      },
      {
        path: "/ops/deployments",
        name: "ops-deployments",
        component: () => import("@/views/ops/deployments/index.vue"),
        meta: { title: "ops-deployments" }
      },
      {
        path: "/ops/hosts",
        name: "ops-hosts",
        component: () => import("@/views/ops/host-workbench/index.vue"),
        meta: { title: "ops-hosts" }
      },
      {
        path: "/ops/storage/nodes",
        name: "ops-storage",
        component: () => import("@/views/ops/storage/index.vue"),
        meta: { title: "ops-storage" }
      }
    ]
  }
];

export const notFoundAndNoPower = [
  {
    path: "/401",
    name: "no-access",
    component: () => import("@/views/error/401.vue"),
    meta: { title: "no-access", hide: true }
  },
  {
    path: "/500",
    name: "no-network",
    component: () => import("@/views/error/500.vue"),
    meta: { title: "no-network", hide: true }
  },
  {
    path: "/:path(.*)*",
    name: "not-found",
    component: () => import("@/views/error/404.vue"),
    meta: { title: "not-found", hide: true }
  }
];
