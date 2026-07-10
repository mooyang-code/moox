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
        path: "/settings/service-deployments",
        name: "settings-service-deployments",
        component: () => import("@/views/settings/service-deployments/index.vue"),
        meta: { title: "settings-service-deployments" }
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
        redirect: { path: "/collector/datasets", query: { tab: "definitions" } },
        meta: { title: "collector-datasets", hide: true }
      },
      {
        path: "/data/fields",
        name: "data-fields",
        component: () => import("@/views/data/fields/index.vue"),
        meta: { title: "data-fields" }
      },
      {
        path: "/data/factors",
        redirect: "/factor/definitions",
        meta: { title: "factor-definitions", hide: true }
      },
      {
        path: "/factor/definitions",
        name: "factor-definitions",
        component: () => import("@/views/factor/definitions/index.vue"),
        meta: { title: "factor-definitions" }
      },
      {
        path: "/factor/bindings",
        name: "factor-bindings",
        component: () => import("@/views/factor/bindings/index.vue"),
        meta: { title: "factor-bindings" }
      },
      {
        path: "/factor/results",
        name: "factor-results",
        component: () => import("@/views/factor/results/index.vue"),
        meta: { title: "factor-results" }
      },
      {
        path: "/data/views",
        redirect: { path: "/collector/views", query: { tab: "definitions" } },
        meta: { title: "collector-views", hide: true }
      },
      {
        path: "/data/view-browse",
        redirect: { path: "/collector/views", query: { tab: "browse" } },
        meta: { title: "collector-views", hide: true }
      },
      {
        path: "/data/overview",
        redirect: "/collector/datasets",
        meta: { title: "collector-datasets", hide: true }
      },
      {
        path: "/data/list",
        redirect: { path: "/collector/datasets", query: { tab: "browse" } },
        meta: { title: "collector-datasets", hide: true }
      },
      {
        path: "/data/browse",
        redirect: { path: "/collector/datasets", query: { tab: "browse" } },
        meta: { title: "collector-datasets", hide: true }
      },
      {
        path: "/data/import",
        name: "data-import",
        component: () => import("@/views/data/import/index.vue"),
        meta: { title: "data-import" }
      },
      {
        path: "/collector/functions",
        redirect: "/collector/cloudnodes",
        meta: { title: "collector-cloudnodes", hide: true }
      },
      {
        path: "/collector/datasets",
        name: "collector-datasets",
        component: () => import("@/views/collector/datasets/index.vue"),
        meta: { title: "collector-datasets" }
      },
      {
        path: "/collector/views",
        name: "collector-views",
        component: () => import("@/views/collector/views/index.vue"),
        meta: { title: "collector-views" }
      },
      {
        path: "/collector/cloudnodes",
        name: "collector-cloudnodes",
        component: () => import("@/views/collector/cloud-node/cloud-node.vue"),
        meta: { title: "collector-cloudnodes" }
      },
      {
        path: "/collector/packages",
        name: "collector-packages",
        component: () => import("@/views/collector/cloud-node/function-package-manage.vue"),
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
        path: "/ops/service-monitor",
        name: "ops-service-monitor",
        component: () => import("@/views/ops/service-monitor/index.vue"),
        meta: { title: "ops-service-monitor" }
      },
      {
        path: "/ops/metric-monitor",
        name: "ops-metric-monitor",
        component: () => import("@/views/ops/metric-monitor/index.vue"),
        meta: { title: "ops-metric-monitor" }
      },
      {
        path: "/ops/resource-monitor",
        name: "ops-resource-monitor",
        component: () => import("@/views/container/resource-monitor/resource-monitor.vue"),
        meta: { title: "ops-resource-monitor" }
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
