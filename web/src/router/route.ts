import { HOME_PATH } from "@/config/index";
import Layout from "@/layout/index.vue";
/**
 * 路由path路径与文件夹名称相同，找文件可以浏览器地址快速查找，方便定位文件
 *
 * 路由meta对象参数，我们通常将属性放到meta对象中
 * meta: {
 *   title:     菜单栏以及 tabsView 栏、菜单搜索名称（国际化）
 *   hide:      是否隐藏此路由，不会显示在菜单树，可以访问
 *   disable:   是否停用，不会显示在菜单树，且不可访问
 *   keepAlive: 是否缓存组件状态
 *   affix:     是否固定在 tabsView 栏上
 *   link:      是否是超链接菜单，开启外链条件：1、 link：链接地址不为空  2、iframe: false
 *   iframe:    是否内嵌窗口，开启条件：1、iframe：true  2、link：链接地址不为空
 *   roles:     当前路由权限表示，取角色管理。路由控制显示、隐藏。 超级管理员：admin；普通角色：common
 *   icon:      菜单、tabsView 图标等
 *   svgIcon:   svg图标
 *   sort:      菜单顺序
 * }
 */

/**
 * 静态路由 （默认路由）
 * 此路由不要动，用于做静态路由定向，如果要添加路由，请在 `layout-children` 中添加
 * @description 前端控制路由 直接改 mock/_data/system_menu 中的路由，后端控制则不需要
 * @returns 返回路由菜单数据
 */
export const staticRoutes = [
  {
    path: "/",
    redirect: HOME_PATH
  },
  {
    path: "/login",
    name: "login",
    component: () => import("@/views/login/login.vue"),
    meta: {
      title: "login"
    }
  },
  {
    path: "/layout",
    name: "layout",
    redirect: HOME_PATH,
    component: Layout, // 容器布局-顶层路由
    // 二级路由-主要渲染页面
    children: [
      {
        path: "/home",
        name: "home",
        component: () => import("@/views/home/home.vue"),
        meta: { title: "首页" }
      },
      {
        path: "/personal/userinfo",
        name: "userinfo",
        component: () => import("@/views/personal/userinfo/userinfo.vue"),
        meta: { title: "个人信息" }
      },
      {
        path: "/personal/user-settings",
        name: "user-settings",
        component: () => import("@/views/personal/user-settings/user-settings.vue"),
        meta: { title: "个人设置" }
      },
      {
        path: "/project/:projectId/dataset",
        name: "dataset",
        component: () => import("@/views/project/dataset/dataset.vue"),
        meta: { title: "数据集" }
      },
      {
        path: "/project/:projectId/field-management",
        name: "field-management",
        component: () => import("@/views/project/field-management/field-management.vue"),
        meta: { title: "字段管理" }
      },
      {
        path: "/project/:projectId/storage-config",
        name: "storage-config",
        component: () => import("@/views/project/storage-config/storage-config.vue"),
        meta: { title: "存储配置" }
      },
      {
        path: "/data-management/:projectId/object-list",
        name: "data-object-list",
        component: () => import("@/views/data/object-list/object-list.vue"),
        meta: { title: "对象列表" }
      },
      {
        path: "/data-management/:projectId/data-list",
        name: "data-data-list",
        component: () => import("@/views/data/data-list/data-list.vue"),
        meta: { title: "数据列表" }
      },
      {
        path: "/data-management/:projectId/overview",
        name: "data-overview",
        component: () => import("@/views/data/overview/overview.vue"),
        meta: { title: "数据概览" }
      },
      {
        path: "/data-management/:projectId/sync",
        name: "data-sync",
        component: () => import("@/views/data/sync/sync.vue"),
        meta: { title: "数据同步" }
      },
      {
        path: "/project/create-project",
        name: "create-project",
        component: () => import("@/views/project/create-project/create-project.vue"),
        meta: { title: "新建项目", keepAlive: false }
      },
      {
        path: "/container-management/container-list",
        name: "container-list",
        component: () => import("@/views/container/container-list/container-list.vue"),
        meta: { title: "容器列表" }
      },
      {
        path: "/container-management/resource-monitor",
        name: "resource-monitor",
        component: () => import("@/views/container/resource-monitor/resource-monitor.vue"),
        meta: { title: "容器监控" }
      },
      {
        path: "/container-management/service-status",
        name: "service-status",
        component: () => import("@/views/container/service-status/service-status.vue"),
        meta: { title: "运行状态", hide: true }
      },
      {
        path: "/factor/factor-list",
        name: "factor-list",
        component: () => import("@/views/factor/factor-list/factor-list.vue"),
        meta: { title: "因子列表" }
      },
      {
        path: "/factor/cloud-function",
        name: "factor-cloud-function",
        component: () => import("@/views/collector/cloud-function/cloud-function.vue"),
        meta: { title: "云函数" }
      },
      {
        path: "/factor/collector-rules",
        name: "factor-collector-rules",
        component: () => import("@/views/collector/collector-rules/collector-rules.vue"),
        meta: { title: "计算规则" }
      },
      {
        path: "/factor/task-instances",
        name: "factor-task-instances",
        component: () => import("@/views/collector/task-instances/task-instances.vue"),
        meta: { title: "任务实例" }
      }
    ]
  }
  /**
   * 提示：写在这里的为全屏界面，不建议写在这里非全屏界面，请写在 layout.children 路由数组中
   *
   */
];

/**
 * 定义401、404、500界面
 * 401无权限
 * 404页面不存在
 * 500网络断开
 * @link 参考：https://router.vuejs.org/zh/guide/essentials/history-mode.html#netlify
 */
export const notFoundAndNoPower = [
  {
    path: "/401", // 无权限，跳转401
    name: "no-access",
    component: () => import("@/views/error/401.vue"),
    meta: {
      title: "no-access",
      hide: true
    }
  },
  {
    path: "/500", // 无网络-浏览器离线
    name: "no-network",
    component: () => import("@/views/error/500.vue"),
    meta: {
      title: "no-network",
      hide: true
    }
  },
  {
    path: "/:path(.*)*", // 匹配任意路由，兜底，未找到页面的时候跳转该页面
    name: "not-found",
    component: () => import("@/views/error/404.vue"),
    meta: {
      title: "not-found",
      hide: true
    }
  }
];
