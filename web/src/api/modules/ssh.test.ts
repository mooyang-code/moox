import { beforeEach, describe, expect, it, vi } from "vitest";

const { callControl } = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl }));
vi.mock("@/api/gateway", () => ({
  gatewayURL: (path: string) => path,
  gatewayWebSocketURL: (path: string) => path
}));

import { createSSHSession, getSftpDownloadUrl, getSftpUploadUrl, getSSHWebSocketUrl } from "./ssh";

// 终端与文件上传下载和页面同源，浏览器自动带会话 Cookie：地址里只有 SSH 会话，不再先取票据。
describe("raw SSH URLs", () => {
  beforeEach(() => callControl.mockReset());

  it("builds WebSocket, download and upload URLs without tickets or extra requests", () => {
    expect(getSSHWebSocketUrl("ssh-1", 80, 24)).toBe("/api/admin/ssh/WsConnect?session_id=ssh-1&w=80&h=24");
    expect(getSftpDownloadUrl("ssh-2", "/tmp/a b&c")).toBe(
      "/api/admin/ssh/SftpDownload?session_id=ssh-2&path=%2Ftmp%2Fa%20b%26c"
    );
    expect(getSftpUploadUrl("ssh-3")).toBe("/api/admin/ssh/SftpUpload?session_id=ssh-3");
    expect(callControl).not.toHaveBeenCalled();
  });
});

describe("SSH session creation", () => {
  it("passes the registered host id as a string", async () => {
    callControl.mockReset().mockResolvedValue({ session_id: "ssh-9" });
    await createSSHSession({ host_id: "control" });
    expect(callControl).toHaveBeenCalledWith("ssh", "CreateSession", { host_id: "control" });
  });
});
