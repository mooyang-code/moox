import { ref } from "vue";
import { defineStore } from "pinia";
import { getUserInfoAPI, logoutAPI } from "@/api/modules/user/index";
import { setSessionExpiredListener } from "@/api/admin/space-client";

// 系统只有一个超级用户：没有角色、权限与个人资料，登录即拥有全部功能。
// 登录会话在 HttpOnly Cookie 里，这里不存任何令牌；是否登录以 GetUserInfo 的结果为准。
interface Account {
  user: {
    id?: string;
    userName?: string;
    loginIp?: string;
  };
}

const userInfoStore = () => {
  const account = ref<Account>({ user: {} });

  function clearAccount() {
    account.value = { user: {} };
  }

  /** 请求一次 GetUserInfo：Cookie 有效就拿到用户，否则抛错（调用方去登录页）。 */
  async function setAccount() {
    const data = await getUserInfoAPI();
    const userInfo = data?.user_info;
    if (!userInfo) throw new Error("获取用户信息失败：响应格式错误");
    account.value = {
      user: {
        id: userInfo.user_id || "",
        userName: userInfo.username || "",
        loginIp: userInfo.last_login_ip || ""
      }
    };
  }

  /** 退出登录：服务端删会话并清 Cookie；服务端不可达时页面这边照样退出。 */
  async function logOut() {
    try {
      await logoutAPI();
    } catch {
      // 页面这边的退出以本地为准
    }
    clearAccount();
  }

  // 任何接口返回 401 时清掉账号，路由守卫重新判断登录。
  setSessionExpiredListener(clearAccount);

  return { account, setAccount, clearAccount, logOut };
};

export const useUserInfoStore = defineStore("user-info", userInfoStore);
