import { flushPromises, mount, type VueWrapper } from "@vue/test-utils";
import ArcoVue from "@arco-design/web-vue";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { AuthSessionExpiredError } from "@/api/admin/auth-errors";
import type { SftpFileItem } from "@/api/modules/ssh";

const mocks = vi.hoisted(() => ({
  sftpList: vi.fn(),
  sftpMkdir: vi.fn(),
  sftpDelete: vi.fn(),
  sftpDownload: vi.fn(),
  sftpUpload: vi.fn(),
  saveBlob: vi.fn(),
  msgError: vi.fn(),
  msgSuccess: vi.fn(),
  msgWarning: vi.fn()
}));

vi.mock("vue-router", () => ({ useRoute: () => ({ query: {} }) }));
vi.mock("@/api/modules/ssh", () => ({
  sftpList: mocks.sftpList,
  sftpMkdir: mocks.sftpMkdir,
  sftpDelete: mocks.sftpDelete,
  sftpDownload: mocks.sftpDownload,
  sftpUpload: mocks.sftpUpload
}));
vi.mock("@/utils/download", () => ({ saveBlob: mocks.saveBlob }));
vi.mock("@arco-design/web-vue", async importOriginal => {
  const actual = await importOriginal<typeof import("@arco-design/web-vue")>();
  return {
    ...actual,
    Message: { ...actual.Message, error: mocks.msgError, success: mocks.msgSuccess, warning: mocks.msgWarning }
  };
});

import SshFileManager from "./ssh-file-manager.vue";

Object.defineProperty(window, "matchMedia", {
  writable: true,
  value: vi.fn(() => ({ matches: false, addEventListener: vi.fn(), removeEventListener: vi.fn() }))
});

const fileRow: SftpFileItem = {
  path: "/home/app.log",
  name: "app.log",
  mode: "-rw-r--r--",
  size: 2048,
  modified_at: "1700000000000",
  type: "f"
};

interface ManagerVM {
  uploadFile(option: {
    fileItem: { file?: File };
    onSuccess: (response?: unknown) => void;
    onError: (response?: unknown) => void;
  }): { abort: () => void };
}

let wrapper: VueWrapper;
async function mountManager() {
  mocks.sftpList.mockResolvedValue({ files: [fileRow], paths: [], current_dir: "/home" });
  wrapper = mount(SshFileManager, {
    props: { sessionId: "ssh-1", hostLabel: "控制机" },
    global: {
      plugins: [ArcoVue],
      stubs: {
        "icon-folder": true,
        "icon-left": true,
        "icon-folder-add": true,
        "icon-upload": true,
        "icon-refresh": true,
        "icon-file": true,
        "icon-download": true,
        "icon-delete": true
      }
    },
    attachTo: document.body
  });
  await flushPromises();
  return wrapper;
}

function pendingDownload() {
  const seen: { signal?: AbortSignal; progress?: (received: number, total: number) => void } = {};
  mocks.sftpDownload.mockImplementationOnce(
    (_sid: string, _path: string, _name: string, progress: (received: number, total: number) => void, signal: AbortSignal) =>
      new Promise((_resolve, reject) => {
        Object.assign(seen, { signal, progress });
        signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      })
  );
  return seen;
}

function pendingUpload() {
  const seen: { signal?: AbortSignal } = {};
  mocks.sftpUpload.mockImplementationOnce(
    (_sid: string, _path: string, _file: File, signal: AbortSignal) =>
      new Promise((_resolve, reject) => {
        seen.signal = signal;
        signal.addEventListener("abort", () => reject(new DOMException("aborted", "AbortError")));
      })
  );
  return seen;
}

beforeEach(() => {
  mocks.sftpList.mockReset();
  mocks.sftpMkdir.mockReset();
  mocks.sftpDelete.mockReset();
  mocks.sftpDownload.mockReset();
  mocks.sftpUpload.mockReset();
  mocks.saveBlob.mockReset();
  mocks.msgError.mockReset();
  mocks.msgSuccess.mockReset();
  mocks.msgWarning.mockReset();
});

afterEach(() => {
  wrapper?.unmount();
  document.body.innerHTML = "";
});

describe("SFTP 文件管理", () => {
  it("下载成功时交给浏览器保存；401 会话过期不弹多余错误提示", async () => {
    const blob = new Blob(["log"]);
    mocks.sftpDownload.mockResolvedValueOnce({ blob, fileName: "app.log" });
    await mountManager();

    const downloadBtn = wrapper.findAll("tbody button").find(btn => btn.text().includes("下载"))!;
    await downloadBtn.trigger("click");
    await flushPromises();
    expect(mocks.sftpDownload).toHaveBeenCalledWith(
      "ssh-1",
      "/home/app.log",
      "app.log",
      expect.any(Function),
      expect.any(AbortSignal)
    );
    expect(mocks.saveBlob).toHaveBeenCalledWith(blob, "app.log");

    mocks.sftpDownload.mockRejectedValueOnce(new AuthSessionExpiredError());
    await downloadBtn.trigger("click");
    await flushPromises();
    expect(mocks.msgError).not.toHaveBeenCalled();
  });

  it("下载中再次点击按钮立即取消并中止底层请求，不保存也不报错", async () => {
    const seen = pendingDownload();
    await mountManager();

    const downloadBtn = () =>
      wrapper.findAll("tbody button").find(btn => btn.text().includes("下载") || btn.text().includes("取消"))!;
    await downloadBtn().trigger("click");
    await flushPromises();
    seen.progress?.(40, 100);
    await flushPromises();
    expect(downloadBtn().text()).toContain("40% · 取消");

    await downloadBtn().trigger("click");
    await flushPromises();
    expect(seen.signal?.aborted).toBe(true);
    expect(mocks.saveBlob).not.toHaveBeenCalled();
    expect(mocks.msgError).not.toHaveBeenCalled();
    expect(downloadBtn().text()).toContain("下载");
  });

  it("uploadFile 同步返回可用 abort 句柄，并支持通过工具栏“取消上传”与卸载中止请求", async () => {
    await mountManager();
    const vm = wrapper.vm as unknown as ManagerVM;
    const file = new File(["payload"], "demo.bin");

    // 1. 校验同步返回 { abort }（非 Promise）并能中止 fetch
    const first = pendingUpload();
    const onSuccess1 = vi.fn();
    const onError1 = vi.fn();
    const handle = vm.uploadFile({ fileItem: { file }, onSuccess: onSuccess1, onError: onError1 });
    expect(handle).not.toBeInstanceOf(Promise);
    expect(typeof handle.abort).toBe("function");
    await flushPromises();
    expect(wrapper.findAll("button").some(btn => btn.text().includes("取消上传"))).toBe(true);

    handle.abort();
    await flushPromises();
    expect(first.signal?.aborted).toBe(true);
    expect(onSuccess1).not.toHaveBeenCalled();
    expect(onError1).not.toHaveBeenCalled();
    expect(wrapper.findAll("button").some(btn => btn.text().includes("取消上传"))).toBe(false);

    // 2. 通过工具栏“取消上传”按钮中止
    const second = pendingUpload();
    const onError2 = vi.fn();
    vm.uploadFile({ fileItem: { file }, onSuccess: vi.fn(), onError: onError2 });
    await flushPromises();
    const cancelBtn = wrapper.findAll("button").find(btn => btn.text().includes("取消上传"))!;
    await cancelBtn.trigger("click");
    await flushPromises();
    expect(second.signal?.aborted).toBe(true);
    expect(onError2).not.toHaveBeenCalled();

    // 3. 上传遇到 401 会话失效时不触发 onError
    mocks.sftpUpload.mockRejectedValueOnce(new AuthSessionExpiredError());
    const onError3 = vi.fn();
    vm.uploadFile({ fileItem: { file }, onSuccess: vi.fn(), onError: onError3 });
    await flushPromises();
    expect(onError3).not.toHaveBeenCalled();
  });

  it("组件卸载时自动中止进行中的下载与上传", async () => {
    const dl = pendingDownload();
    const ul = pendingUpload();
    await mountManager();
    const vm = wrapper.vm as unknown as ManagerVM;

    await wrapper
      .findAll("tbody button")
      .find(btn => btn.text().includes("下载"))!
      .trigger("click");
    vm.uploadFile({ fileItem: { file: new File(["x"], "x.bin") }, onSuccess: vi.fn(), onError: vi.fn() });
    await flushPromises();

    wrapper.unmount();
    await flushPromises();
    expect(dl.signal?.aborted).toBe(true);
    expect(ul.signal?.aborted).toBe(true);
    expect(mocks.saveBlob).not.toHaveBeenCalled();
    expect(mocks.msgError).not.toHaveBeenCalled();
  });

  it("切换会话后重新下载同路径文件时，旧下载迟到的进度回调与 finally 不清空新下载状态", async () => {
    let rejectOld!: (reason?: unknown) => void;
    let oldProgress: ((received: number, total: number) => void) | undefined;
    let resolveNew!: (value: { blob: Blob; fileName: string }) => void;
    let newProgress: ((received: number, total: number) => void) | undefined;

    mocks.sftpDownload
      .mockImplementationOnce(
        (_sid: string, _path: string, _name: string, progress: (received: number, total: number) => void) =>
          new Promise((_resolve, reject) => {
            oldProgress = progress;
            rejectOld = reject;
          })
      )
      .mockImplementationOnce(
        (_sid: string, _path: string, _name: string, progress: (received: number, total: number) => void) =>
          new Promise(resolve => {
            newProgress = progress;
            resolveNew = resolve;
          })
      );

    await mountManager();
    const downloadBtn = () =>
      wrapper.findAll("tbody button").find(btn => btn.text().includes("下载") || btn.text().includes("取消"))!;

    // 1. 在 ssh-1 发起下载（模拟 abort 后底层延迟 reject）
    await downloadBtn().trigger("click");
    await flushPromises();

    // 2. 切到 ssh-2，并立即下载同名同路径文件
    await wrapper.setProps({ sessionId: "ssh-2" });
    await flushPromises();
    await downloadBtn().trigger("click");
    await flushPromises();
    newProgress?.(35, 100);
    await flushPromises();
    expect(downloadBtn().text()).toContain("35% · 取消");

    // 3. 旧下载迟到的进度回调与 finally 均不应覆盖或清除新下载状态
    oldProgress?.(90, 100);
    rejectOld(new DOMException("aborted", "AbortError"));
    await flushPromises();
    expect(downloadBtn().text()).toContain("35% · 取消");

    // 4. 新下载最终正常完成并保存
    const freshBlob = new Blob(["fresh"]);
    resolveNew({ blob: freshBlob, fileName: "app.log" });
    await flushPromises();
    expect(mocks.saveBlob).toHaveBeenCalledWith(freshBlob, "app.log");
    expect(downloadBtn().text()).toContain("下载");
  });
});
