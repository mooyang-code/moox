import { defineStore } from "pinia";
import persistedstateConfig from "@/store/config/index";
import { getUserInfoAPI } from "@/api/modules/user/index";

interface Account {
  user: any; // 用户信息
  roles: string[]; // 角色
  permissions: string[]; // 权限
}

/**
 * 用户角色枚举值映射
 * 根据proto定义：
 * GUEST = 0;       // 游客
 * USER = 1;        // 普通用户  
 * ADMIN = 2;       // 管理员
 * SUPER_ADMIN = 3; // 超级管理员
 */
const mapUserRoleToString = (roleValue: number): string[] => {
  switch (roleValue) {
    case 0: return ["guest"];           // 游客
    case 1: return ["common"];          // 普通用户
    case 2: return ["admin"];           // 管理员
    case 3: return ["admin"];           // 超级管理员，也归类为admin权限
    default: return ["guest"];          // 默认游客权限
  }
};

/**
 * 判断是否为管理员角色
 * UserRole值为2或3为管理员
 */
const isAdminRole = (roleValue: number): boolean => {
  return roleValue === 2 || roleValue === 3;
};

/**
 * 用户信息
 * @methods setAccount 设置账号信息
 * @methods setToken 设置token
 * @methods logOut 退出登录
 */
const userInfoStore = () => {
  // 账号信息
  const account = ref<Account>({
    user: {}, // 用户信息
    roles: [], // 角色
    permissions: [] // 权限
  });
  // token
  const token = ref<string>("");

  // 设置账号信息
  async function setAccount() {
    try {
      // 使用当前存储的token获取用户信息
      if (!token.value) {
        console.error("setAccount: 未找到访问令牌，无法获取用户信息");
        throw new Error("未找到访问令牌，请重新登录");
      }
      
      console.log("setAccount: 开始获取用户信息，token:", token.value.substring(0, 20) + "...");
      
      let response = await getUserInfoAPI(token.value);
      
      console.log("setAccount: 后台响应:", response);
      
      // 使用新的ret_info协议格式
      const data = response.data || response;
      
      // 添加安全检查
      if (!data) {
        throw new Error('获取用户信息失败：响应数据为空');
      }
      
      if (!data.ret_info) {
        throw new Error('获取用户信息失败：响应格式错误，缺少ret_info字段');
      }
      
      // 检查ret_info
      if (data.ret_info.code === 0 && data.user_info) {
        const userInfo = data.user_info;
        
        console.log("setAccount: 用户信息:", userInfo);
        
        // 根据UserRole枚举值判断角色
        const roleStrings = mapUserRoleToString(userInfo.role);
        const isAdmin = isAdminRole(userInfo.role);
        
        console.log("setAccount: 用户角色映射", {
          originalRole: userInfo.role,
          mappedRoles: roleStrings,
          isAdmin: isAdmin
        });
        
        account.value = {
          user: {
            id: userInfo.user_id || "",
            userName: userInfo.username || "",
            nickName: userInfo.nickname || "",
            email: userInfo.email || "",
            phone: userInfo.phone || "",
            avatar: userInfo.avatar || "",
            status: userInfo.status || 0,
            role: userInfo.role || 0,
            roles: roleStrings,
            admin: isAdmin,
            loginIp: userInfo.last_login_ip || "",
            loginDate: userInfo.last_login_at ? new Date(userInfo.last_login_at * 1000).toISOString() : "",
            createTime: userInfo.created_at ? new Date(userInfo.created_at * 1000).toISOString() : ""
          },
          roles: roleStrings,
          permissions: isAdmin ? ["*:*:*"] : []
        };
        
        console.log("setAccount: 用户信息设置成功", account.value);
      } else {
        const errorMessage = data.ret_info.msg || "获取用户信息失败：响应格式错误";
        console.error("setAccount: API响应错误", {
          code: data.ret_info.code,
          message: data.ret_info.msg,
          hasUserInfo: !!data?.user_info,
          response: response
        });
        throw new Error(errorMessage);
      }
    } catch (error: any) {
      console.error("setAccount: 获取用户信息失败", error);
      
      // 清空用户信息，避免死循环
      account.value = {
        user: {},
        roles: [],
        permissions: []
      };
      
      // 关键修复：获取用户信息失败时，完全清除token状态，避免路由守卫死循环
      console.log("setAccount: 获取用户信息失败，清除所有token和用户数据，避免死循环");
      token.value = "";
      
      // 同时清除localStorage中的持久化数据
      try {
        localStorage.removeItem('user-info');
        console.log("setAccount: 已清除localStorage中的用户信息");
      } catch (storageError) {
        console.error("setAccount: 清除localStorage失败", storageError);
      }
      
      // 抛出一个特殊的错误标识，让路由守卫知道需要跳转登录页
      const authError = new Error("获取用户信息失败，请重新登录");
      authError.name = "AuthenticationError";
      throw authError;
    }
  }
  
  // 设置token
  async function setToken(data: string) {
    token.value = data;
  }
  
  // 退出登录
  async function logOut() {
    // 清除账号数据
    account.value = {
      user: {},
      roles: [],
      permissions: []
    };
    token.value = "";
  }

  return { account, token, setAccount, setToken, logOut };
};

export const useUserInfoStore = defineStore("user-info", userInfoStore, {
  persist: persistedstateConfig("user-info", ["token"])
});
