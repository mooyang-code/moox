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
      meta: { title: menu.title, reuseOnQuery: true, env }
    },
    {
      path: `${menu.path}/:logical_account_id`,
      name: `${menu.name}-detail`,
      component: () => import("@/views/trading/accounts/detail/index.vue"),
      meta: {
        title: "交易账户详情",
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
    meta: { title: "登录" }
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
        meta: { title: "首页" }
      },
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
      },
      {
        path: "/events",
        name: "events",
        component: () => import("@/views/events/index.vue"),
        meta: { title: "事件中心", reuseOnQuery: true }
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
        meta: { title: "数据对象", reuseOnQuery: true }
      },
      {
        path: "/data/fields",
        name: "data-fields",
        component: () => import("@/views/data/fields/index.vue"),
        meta: { title: "基础字段", reuseOnQuery: true }
      },
      {
        path: "/archive/tasks",
        name: "archive-tasks",
        component: () => import("@/views/archive/tasks/index.vue"),
        meta: { title: "归档任务", reuseOnQuery: true }
      },
      {
        path: "/archive/storage",
        name: "archive-storage",
        component: () => import("@/views/archive/storage/index.vue"),
        meta: { title: "归档存储" }
      },
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
        meta: { title: "因子定义", reuseOnQuery: true }
      },
      {
        path: "/factor/tasks",
        name: "factor-tasks",
        component: () => import("@/views/factor/task-management/index.vue"),
        meta: { title: "计算任务", reuseOnQuery: true }
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
      },
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
        meta: { title: "策略定义", reuseOnQuery: true }
      },
      {
        path: "/strategy/tasks",
        name: "strategy-tasks",
        component: () => import("@/views/strategy/workbench/index.vue"),
        meta: { title: "策略运行", reuseOnQuery: true }
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
      },
      {
        path: "/ops/activity",
        name: "ops-activity",
        component: () => import("@/views/ops/activity/index.vue"),
        meta: { title: "活动日志" }
      },
      {
        path: "/collector/tasks",
        name: "collector-tasks",
        component: () => import("@/views/collector/task-management/index.vue"),
        meta: { title: "采集任务", reuseOnQuery: true }
      },
      ...tradingAccountRoutes,
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
        meta: {
          title: "交易账户详情",
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
        meta: { title: "监控告警", reuseOnQuery: true }
      },
      {
        path: "/ops/logs",
        name: "ops-logs",
        component: () => import("@/views/ops/log-records/index.vue"),
        meta: { title: "重要日志" }
      },
      {
        path: "/ops/deployments",
        name: "ops-deployments",
        component: () => import("@/views/ops/deployments/index.vue"),
        meta: { title: "服务", reuseOnQuery: true }
      },
      {
        path: "/ops/hosts",
        name: "ops-hosts",
        component: () => import("@/views/ops/host-workbench/index.vue"),
        meta: { title: "主机工作台", reuseOnQuery: true }
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
    meta: { title: "未找到页面", hide: true }
  }
];
