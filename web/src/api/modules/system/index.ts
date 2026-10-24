import { deepClone, filterByDisable, buildTreeOptimized, treeSort } from "./menu-utils";
import { systemMenu } from "./static-menu";

// 获取菜单数据：系统只有一个超级用户，菜单不按角色过滤，只去掉被禁用的项
export const getRoutersAPI = () => {
  const originMenu: any = deepClone(systemMenu);
  const survivalTree = filterByDisable(originMenu);
  const filteredMenu = treeSort(buildTreeOptimized(survivalTree));

  // 返回调用方使用的本地菜单响应结构。
  return Promise.resolve({
    data: filteredMenu,
    status: 200,
    statusText: "OK"
  });
};
