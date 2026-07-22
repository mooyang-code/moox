import { HOME_PATH } from "@/config/index";
import Layout from "@/layout/index.vue";

const collectorDataManagementPath = "/collector/data-management";

function redirectCollectorDatasets(to: { query?: Record<string, unknown> }) {
  const query: Record<string, unknown> = {
    tab: "datasets",
    datasetTab: to.query?.tab === "browse" ? "browse" : "definitions"
  };
  for (const key of ["space_id", "dataset_id"]) {
    if (to.query?.[key] !== undefined) query[key] = to.query[key];
  }
  return {
    path: collectorDataManagementPath,
    query
  };
}

function redirectCollectorViews(to: { query?: Record<string, unknown> }) {
  return {
    path: collectorDataManagementPath,
    query: {
      tab: "views",
      viewTab: to.query?.tab === "browse" ? "browse" : "definitions"
    }
  };
}

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
        redirect: { path: "/ops/services", query: { tab: "deployments" } },
        meta: { title: "settings-service-deployments", hide: true }
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
        redirect: redirectCollectorDatasets,
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
        path: "/strategy/overview",
        name: "strategy-overview",
        component: () => import("@/views/strategy/overview/index.vue"),
        meta: { title: "strategy-overview" }
      },
      {
        path: "/strategy/running",
        name: "strategy-running",
        component: () => import("@/views/strategy/running/index.vue"),
        meta: { title: "strategy-running" }
      },
      {
        path: "/strategy/detail/:bindingId",
        name: "strategy-detail",
        component: () => import("@/views/strategy/detail/index.vue"),
        meta: { title: "strategy-detail", hide: true }
      },
      {
        path: "/data/views",
        redirect: redirectCollectorViews,
        meta: { title: "collector-views", hide: true }
      },
      {
        path: "/data/view-browse",
        redirect: () => ({
          path: collectorDataManagementPath,
          query: { tab: "views", viewTab: "browse" }
        }),
        meta: { title: "collector-views", hide: true }
      },
      {
        path: "/data/overview",
        redirect: redirectCollectorDatasets,
        meta: { title: "collector-datasets", hide: true }
      },
      {
        path: "/data/list",
        redirect: () => ({
          path: collectorDataManagementPath,
          query: { tab: "datasets", datasetTab: "browse" }
        }),
        meta: { title: "collector-datasets", hide: true }
      },
      {
        path: "/data/browse",
        redirect: () => ({
          path: collectorDataManagementPath,
          query: { tab: "datasets", datasetTab: "browse" }
        }),
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
        path: "/collector/data-management",
        name: "collector-data-management",
        component: () => import("@/views/collector/data-management/index.vue"),
        meta: { title: "collector-data-management" }
      },
      {
        path: "/collector/datasets",
        redirect: redirectCollectorDatasets,
        meta: { title: "collector-datasets", hide: true }
      },
      {
        path: "/collector/views",
        redirect: redirectCollectorViews,
        meta: { title: "collector-views", hide: true }
      },
      {
        path: "/collector/cloudnodes",
        name: "collector-cloudnodes",
        component: () => import("@/views/collector/cloud-node/cloud-node.vue"),
        meta: { title: "collector-cloudnodes" }
      },
      {
        path: "/collector/packages",
        redirect: "/collector/cloudnodes",
        meta: { title: "collector-cloudnodes", hide: true }
      },
      {
        path: "/collector/rules",
        name: "collector-rules",
        component: () => import("@/views/collector/task-management/index.vue"),
        meta: { title: "collector-rules" }
      },
      {
        path: "/collector/tasks",
        redirect: { path: "/collector/rules", query: { tab: "instances" } },
        meta: { title: "collector-rules", hide: true }
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
        path: "/ops/services",
        name: "ops-services",
        component: () => import("@/views/ops/service-management/index.vue"),
        meta: { title: "ops-services" }
      },
      {
        path: "/ops/hosts",
        name: "ops-hosts",
        component: () => import("@/views/ops/host-workbench/index.vue"),
        meta: { title: "ops-hosts" }
      },
      {
        path: "/ops/service-monitor",
        redirect: { path: "/ops/services", query: { tab: "availability" } },
        meta: { title: "ops-service-monitor", hide: true }
      },
      {
        path: "/ops/metric-monitor",
        redirect: { path: "/ops/services", query: { tab: "metrics" } },
        meta: { title: "ops-metric-monitor", hide: true }
      },
      {
        path: "/ops/resource-monitor",
        redirect: { path: "/ops/hosts", query: { tab: "monitor" } },
        meta: { title: "ops-resource-monitor", hide: true }
      },
      {
        path: "/ops/ssh-hosts",
        redirect: { path: "/ops/hosts", query: { tab: "hosts" } },
        meta: { title: "ops-ssh-hosts", hide: true }
      },
      {
        path: "/ops/ssh-terminal",
        redirect: (to: any) => ({
          path: "/ops/hosts",
          query: { tab: "hosts", ...(to.query.hostId ? { hostId: to.query.hostId } : {}) }
        }),
        meta: { title: "ops-ssh-terminal", hide: true }
      },
      {
        path: "/ops/ssh-sessions",
        redirect: { path: "/ops/hosts", query: { tab: "hosts" } },
        meta: { title: "ops-ssh-sessions", hide: true }
      },
      {
        path: "/ops/storage/nodes",
        name: "ops-storage",
        component: () => import("@/views/ops/storage/index.vue"),
        meta: { title: "ops-storage" }
      },
      {
        path: "/ops/storage/archive",
        redirect: { path: "/ops/storage/nodes", query: { tab: "archive" } },
        meta: { title: "ops-storage", hide: true }
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
