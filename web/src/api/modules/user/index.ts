import { callControl } from "@/api/admin/http";
import { secureLoginManager } from "@/utils/crypto";

export const loginAPI = async (data: { username: string; password: string; verifyCode: string }) => {
  return await secureLoginManager.login(data.username, data.password);
};

export const getUserInfoAPI = async (accessToken: string) => {
  try {
    return await callControl<{ user_id: string }, { user_info?: Record<string, any> }>(
      "auth",
      "GetUserInfo",
      {
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
