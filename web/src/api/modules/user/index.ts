import { callControl } from "@/api/admin/http";
import { getAppInfo } from "@/api/storage/auth";
import { secureLoginManager } from "@/utils/crypto";

// 安全登录（新版本）
export const loginAPI = async (data: { username: string; password: string; verifyCode: string }) => {
  // 使用安全登录管理器进行登录
  return await secureLoginManager.login(data.username, data.password);
};

// 获取用户信息 - 调用真实后台接口
export const getUserInfoAPI = async (accessToken: string) => {
  try {
    return await callControl<
      { app_info: ReturnType<typeof getAppInfo>; access_token: string; user_id: string },
      { user_info?: Record<string, any> }
    >(
      "auth",
      "GetUserInfo",
      {
        app_info: getAppInfo(),
        access_token: accessToken,
        user_id: "" // 空字符串表示获取当前用户信息
      },
      {
        headers: {
          Authorization: accessToken,
          "X-Access-Token": accessToken
        }
      }
    );
  } catch (error: unknown) {
    console.error("获取用户信息失败:", error);
    throw error;
  }
};

export const logoutAPI = () => callControl<Record<string, never>, Record<string, never>>("auth", "Logout", {});

export const issueRawSessionTicketAPI = (operation: "ssh_ws" | "sftp_download" | "sftp_upload", sessionId: string) =>
  callControl<{ operation: string; session_id: string }, { ticket: string; expires_at: number }>(
    "auth",
    "IssueRawSessionTicket",
    { operation, session_id: sessionId }
  );
