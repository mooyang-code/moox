import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthSessionExpiredError } from "@/api/admin/auth-errors";
import { setSessionExpiredListener } from "@/api/admin/space-client";

const { call } = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock("@/api/http", () => ({ call }));
vi.mock("@/api/gateway", () => ({
  gatewayURL: (path: string) => path,
  gatewayWebSocketURL: (path: string) => path
}));

import {
  createSSHSession,
  getSftpDownloadUrl,
  getSftpUploadUrl,
  getSSHWebSocketUrl,
  sftpDispositionFileName,
  sftpDownload,
  sftpUpload
} from "./ssh";

const fetchMock = vi.fn();
const json = (status: number, body: unknown) =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

// 终端与文件上传下载和页面同源，浏览器自动带会话 Cookie：地址里只有 SSH 会话，不再先取票据。
describe("raw SSH URLs", () => {
  beforeEach(() => call.mockReset());

  it("builds WebSocket, download and upload URLs without tickets or extra requests", () => {
    expect(getSSHWebSocketUrl("ssh-1", 80, 24)).toBe("/api/admin/ssh/WsConnect?session_id=ssh-1&w=80&h=24");
    expect(getSftpDownloadUrl("ssh-2", "/tmp/a b&c")).toBe(
      "/api/admin/ssh/SftpDownload?session_id=ssh-2&path=%2Ftmp%2Fa%20b%26c"
    );
    expect(getSftpUploadUrl("ssh-3")).toBe("/api/admin/ssh/SftpUpload?session_id=ssh-3");
    expect(call).not.toHaveBeenCalled();
  });
});

describe("SSH session creation", () => {
  it("passes the registered host id as a string", async () => {
    call.mockReset().mockResolvedValue({ session_id: "ssh-9" });
    await createSSHSession({ host_id: "control" });
    expect(call).toHaveBeenCalledWith("ssh", "CreateSession", { host_id: "control" });
  });
});

describe("SFTP 裸 HTTP 上传与下载", () => {
  beforeEach(() => {
    fetchMock.mockReset();
    vi.stubGlobal("fetch", fetchMock);
    window.location.hash = "#/ops/hosts";
  });

  afterEach(() => {
    setSessionExpiredListener(null);
    window.location.hash = "#/";
    vi.unstubAllGlobals();
  });

  it("上传透传 same-origin 与 AbortSignal，401 触发清登录态并跳登录页", async () => {
    const cleared = vi.fn();
    setSessionExpiredListener(cleared);
    const controller = new AbortController();
    const file = new File(["hello"], "a.txt", { type: "text/plain" });

    fetchMock.mockResolvedValueOnce(json(200, { code: 0, msg: "1 个文件上传成功", files: ["a.txt"], success: true }));
    const res = await sftpUpload("ssh-1", "/tmp", file, controller.signal);
    expect(res.success).toBe(true);
    expect(fetchMock).toHaveBeenCalledWith(
      "/api/admin/ssh/SftpUpload?session_id=ssh-1",
      expect.objectContaining({ method: "POST", credentials: "same-origin", signal: controller.signal })
    );

    fetchMock.mockResolvedValueOnce(json(401, { ret_info: { code: 4, msg: "登录已失效，请重新登录" } }));
    await expect(sftpUpload("ssh-1", "/tmp", file)).rejects.toBeInstanceOf(AuthSessionExpiredError);
    expect(cleared).toHaveBeenCalledTimes(1);
    expect(window.location.hash).toBe(`#/login?redirect=${encodeURIComponent("/ops/hosts")}`);

    fetchMock.mockResolvedValueOnce(json(500, { code: 500, msg: "磁盘空间不足", success: false }));
    await expect(sftpUpload("ssh-1", "/tmp", file)).rejects.toThrow("磁盘空间不足");
  });

  it("下载支持进度回调、文件名解析、401 跳登录与流读取中途取消", async () => {
    const cleared = vi.fn();
    setSessionExpiredListener(cleared);

    // 正常流式下载（兼容后端不带引号的 Content-Disposition）
    fetchMock.mockResolvedValueOnce(
      new Response(new Uint8Array([1, 2, 3, 4]), {
        status: 200,
        headers: { "Content-Length": "4", "Content-Disposition": "attachment; filename=app.log" }
      })
    );
    const progress = vi.fn();
    const downloaded = await sftpDownload("ssh-2", "/var/log/app.log", "fallback.log", progress);
    expect(downloaded.fileName).toBe("app.log");
    expect(downloaded.blob.size).toBe(4);
    expect(progress).toHaveBeenLastCalledWith(4, 4);

    // 401 会话过期跳登录
    fetchMock.mockResolvedValueOnce(json(401, { ret_info: { code: 4, msg: "登录已失效，请重新登录" } }));
    await expect(sftpDownload("ssh-2", "/var/log/app.log", "app.log")).rejects.toBeInstanceOf(AuthSessionExpiredError);
    expect(cleared).toHaveBeenCalledTimes(1);
    expect(window.location.hash).toBe(`#/login?redirect=${encodeURIComponent("/ops/hosts")}`);

    // 流式读取中途通过 AbortController 取消，立刻 cancel reader 并以 AbortError 结束
    let cancelCalled = false;
    let firstChunkSent = false;
    const stream = new ReadableStream<Uint8Array>({
      pull(ctrl) {
        if (!firstChunkSent) {
          firstChunkSent = true;
          ctrl.enqueue(new Uint8Array([1, 2]));
          return;
        }
        return new Promise(() => undefined);
      },
      cancel() {
        cancelCalled = true;
      }
    });
    fetchMock.mockResolvedValueOnce(new Response(stream, { status: 200, headers: { "Content-Length": "100" } }));
    const controller = new AbortController();
    const pending = sftpDownload(
      "ssh-2",
      "/var/log/big.bin",
      "big.bin",
      () => {
        controller.abort();
      },
      controller.signal
    );
    await expect(pending).rejects.toMatchObject({ name: "AbortError" });
    expect(cancelCalled).toBe(true);
  });

  it("兼容带引号与含空格的未加引号 Content-Disposition 文件名", () => {
    expect(sftpDispositionFileName('attachment; filename="a b.txt"')).toBe("a b.txt");
    expect(sftpDispositionFileName("attachment; filename=report.csv")).toBe("report.csv");
    expect(sftpDispositionFileName("attachment; filename=daily report.csv")).toBe("daily report.csv");
    expect(sftpDispositionFileName("attachment; filename=daily report.csv; size=128")).toBe("daily report.csv");
    expect(sftpDispositionFileName(null)).toBe("");
  });

  it("signal 在进入流读取前已处于 aborted 时，finally 会取消响应流释放连接", async () => {
    let cancelCalled = false;
    const stream = new ReadableStream<Uint8Array>({
      pull(ctrl) {
        ctrl.enqueue(new Uint8Array([1, 2, 3]));
      },
      cancel() {
        cancelCalled = true;
      }
    });
    const controller = new AbortController();
    fetchMock.mockImplementationOnce(async () => {
      // 模拟 fetch 返回响应头的同时 signal 已被中止
      controller.abort();
      return new Response(stream, { status: 200, headers: { "Content-Length": "3" } });
    });

    await expect(sftpDownload("ssh-2", "/var/log/a.log", "a.log", undefined, controller.signal)).rejects.toMatchObject({
      name: "AbortError"
    });
    expect(cancelCalled).toBe(true);
  });
});
