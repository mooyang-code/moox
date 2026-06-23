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
        path: "/personal/userinfo",
        name: "userinfo",
        component: () => import("@/views/personal/userinfo/userinfo.vue"),
        meta: { title: "userinfo" }
      },
      {
        path: "/personal/user-settings",
        name: "user-settings",
        component: () => import("@/views/personal/user-settings/user-settings.vue"),
        meta: { title: "user-settings" }
      },
      {
        path: "/settings/spaces",
        name: "settings-spaces",
        component: () => import("@/views/settings/spaces/index.vue"),
        meta: { title: "settings-spaces" }
      },
      {
        path: "/settings/permissions",
        name: "settings-permissions",
        component: () => import("@/views/settings/permissions/index.vue"),
        meta: { title: "settings-permissions" }
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
        path: "/data/datasets",
        name: "data-datasets",
        component: () => import("@/views/data/datasets/index.vue"),
        meta: { title: "data-datasets" }
      },
      {
        path: "/data/fields",
        name: "data-fields",
        component: () => import("@/views/data/fields/index.vue"),
        meta: { title: "data-fields" }
      },
      {
        path: "/data/factors",
        name: "data-factors",
        component: () => import("@/views/data/factors/index.vue"),
        meta: { title: "data-factors" }
      },
      {
        path: "/data/views",
        name: "data-views",
        component: () => import("@/views/data/views/index.vue"),
        meta: { title: "data-view-list" }
      },
      {
        path: "/data/view-browse",
        name: "data-view-browse",
        component: () => import("@/views/data/view-browse/index.vue"),
        meta: { title: "data-view-browse" }
      },
      {
        path: "/data/overview",
        name: "data-overview",
        component: () => import("@/views/data/overview/overview.vue"),
        meta: { title: "data-overview" }
      },
      {
        path: "/data/list",
        redirect: "/data/browse",
        meta: { title: "data-browse", hide: true }
      },
      {
        path: "/data/browse",
        name: "data-browse",
        component: () => import("@/views/data/browse/index.vue"),
        meta: { title: "data-browse" }
      },
      {
        path: "/data/import",
        name: "data-import",
        component: () => import("@/views/data/import/index.vue"),
        meta: { title: "data-import" }
      },
      {
        path: "/collector/functions",
        name: "collector-functions",
        component: () => import("@/views/collector/cloud-function/cloud-function.vue"),
        meta: { title: "collector-functions" }
      },
      {
        path: "/collector/packages",
        name: "collector-packages",
        component: () => import("@/views/collector/cloud-function/function-package-manage.vue"),
        meta: { title: "collector-packages" }
      },
      {
        path: "/collector/rules",
        name: "collector-rules",
        component: () => import("@/views/collector/collector-rules/collector-rules.vue"),
        meta: { title: "collector-rules" }
      },
      {
        path: "/collector/tasks",
        name: "collector-tasks",
        component: () => import("@/views/collector/task-instances/task-instances.vue"),
        meta: { title: "collector-tasks" }
      },
      {
        path: "/strategy/list",
        name: "strategy-list",
        component: () => import("@/views/strategy/strategy-list/strategy-list.vue"),
        meta: { title: "strategy-list" }
      },
      {
        path: "/trading/accounts",
        name: "trading-accounts",
        component: () => import("@/views/trading/account-overview/account-overview.vue"),
        meta: { title: "trading-accounts" }
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
        path: "/ops/resource-monitor",
        name: "ops-resource-monitor",
        component: () => import("@/views/container/resource-monitor/resource-monitor.vue"),
        meta: { title: "ops-resource-monitor" }
      },
      {
        path: "/ops/service-status",
        name: "ops-service-status",
        component: () => import("@/views/container/service-status/service-status.vue"),
        meta: { title: "ops-service-status" }
      },
      {
        path: "/ops/ssh-hosts",
        name: "ops-ssh-hosts",
        component: () => import("@/views/container/ssh-hosts/ssh-hosts.vue"),
        meta: { title: "ops-ssh-hosts" }
      },
      {
        path: "/ops/ssh-terminal",
        name: "ops-ssh-terminal",
        component: () => import("@/views/container/ssh-terminal/ssh-terminal.vue"),
        meta: { title: "ops-ssh-terminal" }
      },
      {
        path: "/ops/ssh-sessions",
        name: "ops-ssh-sessions",
        component: () => import("@/views/container/ssh-sessions/ssh-sessions.vue"),
        meta: { title: "ops-ssh-sessions" }
      },
      {
        path: "/ops/storage/nodes",
        name: "ops-storage-nodes",
        component: () => import("@/views/ops/storage/nodes.vue"),
        meta: { title: "ops-storage-nodes" }
      },
      {
        path: "/ops/storage/routes",
        name: "ops-storage-routes",
        component: () => import("@/views/ops/storage/routes.vue"),
        meta: { title: "ops-storage-routes" }
      },
      {
        path: "/ops/storage/archive",
        name: "ops-storage-archive",
        component: () => import("@/views/ops/storage/archive.vue"),
        meta: { title: "ops-storage-archive" }
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
