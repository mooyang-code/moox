import { callControl } from "@/api/admin/http";
import { secureLoginManager } from "@/utils/crypto";

export const loginAPI = async (data: { username: string; password: string }) => {
  return await secureLoginManager.login(data.username, data.password);
};

export const getUserInfoAPI = async (accessToken: string) => {
  try {
    return await callControl<Record<string, never>, { user_info?: Record<string, any> }>(
      "auth",
      "GetUserInfo",
      {},
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

/**
 * 一次性 ticket 的操作。SSH 三个操作的 sessionId 是 SSH 会话 ID；
 * 归档的下载与最新数据填 "<空间>/<数据集>"，请求里的 space、dataset 必须与它一致，ticket 只能用一次（翻页要重新取）。
 */
export type RawSessionTicketOperation = "ssh_ws" | "sftp_download" | "sftp_upload" | "archive_download" | "archive_recent";

export const issueRawSessionTicketAPI = (operation: RawSessionTicketOperation, sessionId: string) =>
  callControl<{ operation: string; session_id: string }, { ticket: string; expires_at: number }>(
    "auth",
    "IssueRawSessionTicket",
    { operation, session_id: sessionId }
  );
