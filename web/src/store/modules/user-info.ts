import { ref } from "vue";
import { defineStore } from "pinia";
import persistedstateConfig from "@/store/config/index";
import { getUserInfoAPI } from "@/api/modules/user/index";
import { logoutAPI } from "@/api/modules/user/index";
import { clearBrowserSession } from "@/api/admin/signed-client";
import { loadSigningSession, saveSigningSession } from "@/utils/request-signing";

// 系统只有一个超级用户：没有角色、权限与个人资料，登录即拥有全部功能。
interface Account {
  user: {
    id?: string;
    userName?: string;
    loginIp?: string;
    loginDate?: string;
    createTime?: string;
  };
}

/**
 * 用户信息
 * @methods setAccount 设置账号信息
 * @methods setToken 设置token
 * @methods logOut 退出登录
 */
export const STALE_ACCOUNT_REQUEST = "StaleAccountRequestError";

function staleAccountRequestError(): Error {
  const error = new Error("用户信息请求所属的登录会话已变化");
  error.name = STALE_ACCOUNT_REQUEST;
  return error;
}

const userInfoStore = () => {
  // 账号信息
  const account = ref<Account>({ user: {} });
  // token
  const token = ref<string>("");
  const sessionId = ref<string>("");
  const expiresAt = ref<number>(0);

  // 设置账号信息
  async function setAccount() {
    // 固定发起时的会话：等待期间用户重新登录后，旧请求的成功/失败回写都不能影响新会话
    const requestSession = sessionId.value;
    const requestToken = token.value;
    const isStale = () => sessionId.value !== requestSession || token.value !== requestToken;
    try {
      // 使用当前存储的token获取用户信息
      if (!token.value) {
        console.error("setAccount: 未找到访问令牌，无法获取用户信息");
        throw new Error("未找到访问令牌，请重新登录");
      }

      const data = await getUserInfoAPI(requestToken);
      if (isStale()) throw staleAccountRequestError();

      // 添加安全检查
      if (!data) {
        throw new Error("获取用户信息失败：响应数据为空");
      }

      if (data.user_info) {
        const userInfo = data.user_info;
        account.value = {
          user: {
            id: userInfo.user_id || "",
            userName: userInfo.username || "",
            loginIp: userInfo.last_login_ip || "",
            loginDate: userInfo.last_login_at ? new Date(userInfo.last_login_at * 1000).toISOString() : "",
            createTime: userInfo.created_at ? new Date(userInfo.created_at * 1000).toISOString() : ""
          }
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
      if (error?.name === STALE_ACCOUNT_REQUEST || isStale()) {
        // 旧会话的迟到结果：既不覆盖也不清除当前会话，由调用方识别后重新进入当前会话的流程
        throw staleAccountRequestError();
      }
      console.error("setAccount: 获取用户信息失败", error);

      // 清空用户信息，避免死循环
      account.value = { user: {} };

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
    return Boolean(token.value && sessionId.value && expiresAt.value > now && (await loadSigningSession(sessionId.value)));
  }

  // 退出登录
  async function logOut() {
    try {
      if (token.value && sessionId.value) await logoutAPI();
    } catch {
      // Local logout is authoritative even when the server cannot be reached.
    }
    // 清除账号数据
    account.value = { user: {} };
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
