import axios from "@/api";
import { useUserInfoStore } from "@/store/modules/user-info";
import { deepClone, filterByDisable, buildTreeOptimized, treeSort } from "@/mock/_utils";
import { systemMenu } from "@/mock/_data/system_menu";

// 获取菜单数据 - 基于真实用户角色
export const getRoutersAPI = () => {
  // 获取用户信息
  const userStore = useUserInfoStore();
  const { account } = userStore;

  // 根据用户角色判断权限
  // UserRole值为2或3为管理员，否则为普通用户
  const userRoles = account.roles && account.roles.length > 0 ? account.roles : ["guest"];

  console.log("当前用户角色:", userRoles);

  // 使用现有的菜单过滤逻辑
  const originMenu: any = deepClone(systemMenu);
  const survivalTree = filterByDisable(originMenu, userRoles);
  const filteredMenu = treeSort(buildTreeOptimized(survivalTree));

  // 模拟API响应格式
  return Promise.resolve({
    data: filteredMenu,
    status: 200,
    statusText: 'OK'
  });
};

// 获取字典数据
export const getDictAPI = () => {
  return axios({
    url: "/mock/system/getDict",
    method: "get"
  });
};
