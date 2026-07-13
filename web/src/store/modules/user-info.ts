import { defineStore } from "pinia";
import persistedstateConfig from "@/store/config/index";
import { getUserInfoAPI } from "@/api/modules/user/index";
import { logoutAPI } from "@/api/modules/user/index";
import { clearBrowserSession } from "@/api/admin/signed-client";
import { loadSigningSession, saveSigningSession } from "@/utils/request-signing";

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
  const sessionId = ref<string>("");
  const expiresAt = ref<number>(0);

  // 设置账号信息
  async function setAccount() {
    try {
      // 使用当前存储的token获取用户信息
      if (!token.value) {
        console.error("setAccount: 未找到访问令牌，无法获取用户信息");
        throw new Error("未找到访问令牌，请重新登录");
      }
      
      const data = await getUserInfoAPI(token.value);
      
      // 添加安全检查
      if (!data) {
        throw new Error('获取用户信息失败：响应数据为空');
      }
      
      if (data.user_info) {
        const userInfo = data.user_info;
        
        // 根据UserRole枚举值判断角色
        const roleStrings = mapUserRoleToString(userInfo.role);
        const isAdmin = isAdminRole(userInfo.role);
        
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
      } else {
        const errorMessage = "获取用户信息失败：响应格式错误";
        console.error("setAccount: API响应错误", {
          hasUserInfo: !!data?.user_info,
          response: data
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
      token.value = "";
      sessionId.value = "";
      expiresAt.value = 0;
      
      // 同时清除localStorage中的持久化数据
      try {
        await clearBrowserSession();
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

  async function setLoginSession(data: { token: string; sessionId: string; signingKey: string; expiresAt: number }) {
    await saveSigningSession({ sessionId: data.sessionId, rawKeyHex: data.signingKey, expiresAt: data.expiresAt });
    token.value = data.token;
    sessionId.value = data.sessionId;
    expiresAt.value = data.expiresAt;
  }

  async function hasValidSession() {
    const now = Math.floor(Date.now() / 1000);
    return Boolean(token.value && sessionId.value && expiresAt.value > now && await loadSigningSession(sessionId.value));
  }
  
  // 退出登录
  async function logOut() {
    try {
      if (token.value && sessionId.value) await logoutAPI();
    } catch {
      // Local logout is authoritative even when the server cannot be reached.
    }
    // 清除账号数据
    account.value = {
      user: {},
      roles: [],
      permissions: []
    };
    token.value = "";
    sessionId.value = "";
    expiresAt.value = 0;
    await clearBrowserSession();
  }

  return { account, token, sessionId, expiresAt, setAccount, setToken, setLoginSession, hasValidSession, logOut };
};

export const useUserInfoStore = defineStore("user-info", userInfoStore, {
  persist: persistedstateConfig("user-info", ["token", "sessionId", "expiresAt"])
});
