import { describe, expect, it } from "vitest";
import { buildMenuTree } from "@/layout/components/Menu/menu-selected";
import { environmentMenus } from "@/components/trade-confirm/environment";
import { flatRoutes, layoutRoutes } from "./route";

const menu = buildMenuTree(layoutRoutes);
const children = (name: string) => menu.find(item => item.name === name)?.children.map(item => item.title);

describe("菜单由路由表生成", () => {
  it("一级菜单的顺序、标题与图标", () => {
    expect(menu.map(item => item.title)).toEqual([
      "首页",
      "数据采集",
      "因子计算",
      "策略管理",
      "交易管理",
      "数据归档",
      "事件中心",
      "资源与运维",
      "系统设置"
    ]);
    const icons = menu.map(item => item.svgIcon);
    expect(icons.every(Boolean)).toBe(true);
    expect(new Set(icons).size).toBe(icons.length);
  });

  it("各目录下的菜单与顺序；隐藏路由不进菜单", () => {
    expect(children("compute-collector")).toEqual(["采集任务", "数据源", "数据对象", "基础字段"]);
    expect(children("factor-compute")).toEqual(["因子总览", "因子定义", "计算任务"]);
    expect(children("strategy")).toEqual(["策略总览", "策略定义", "策略运行"]);
    expect(children("trading")).toEqual(["模拟账户", "测试账户", "实盘账户", "下架名单"]);
    expect(children("archive")).toEqual(["归档任务", "归档存储"]);
    expect(children("ops")).toEqual(["监控告警", "服务管理", "主机工作台", "日志中心"]);
    expect(children("settings")).toEqual(["空间管理", "密钥管理"]);
    // 事件中心是单页菜单，页签在页内
    expect(menu.find(item => item.name === "events")?.children).toEqual([]);
  });

  it("路由名不重复；隐藏路由指向的高亮菜单存在", () => {
    const routes = flatRoutes();
    const names = routes.map(route => route.name);
    expect(new Set(names).size).toBe(names.length);
    for (const route of routes.filter(item => item.meta.hide)) {
      expect(names, `${route.name} 的 activeMenu`).toContain(route.meta.activeMenu);
    }
  });

  it("交易三环境的菜单取自路由表", () => {
    expect(environmentMenus).toEqual({
      paper: { name: "trading-paper", path: "/trading/paper", title: "模拟账户" },
      testnet: { name: "trading-testnet", path: "/trading/testnet", title: "测试账户" },
      prod: { name: "trading-live", path: "/trading/live", title: "实盘账户" }
    });
    for (const env of ["paper", "testnet", "prod"] as const) {
      const detail = flatRoutes().find(route => route.name === `${environmentMenus[env].name}-detail`);
      expect(detail?.path).toBe(`${environmentMenus[env].path}/:logical_account_id`);
    }
  });
});
