import { createHash } from "node:crypto";
import { afterEach, describe, expect, it, vi } from "vitest";

const mocks = vi.hoisted(() => ({ call: vi.fn() }));
vi.mock("@/api/http", () => ({ call: mocks.call }));

import {
  deleteFunctionPackage,
  formatFileSize,
  listFunctionPackages,
  packageStatusLabel,
  uploadFunctionPackage
} from "./function-package";

afterEach(() => {
  vi.unstubAllGlobals();
});

describe("uploadFunctionPackage", () => {
  it("runs Init -> PUT -> Complete globally without space_id or cloud_account_id", async () => {
    mocks.call.mockReset().mockImplementation(async (_service: string, method: string) => {
      if (method === "InitPackageUpload") {
        return { package: { package_id: "pkg-1" }, upload_url: "https://cos.example/put" };
      }
      return { package: { package_id: "pkg-1", status: "available" } };
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => ({ ok: true, status: 200 }))
    );
    const file = new File(["zip-bytes"], "moox-collector-v1.0.1.zip");
    const result = await uploadFunctionPackage({ package_name: "moox-collector-plan", version: "v1.0.1", runtime: "Go1" }, file);
    expect(result).toMatchObject({ package_id: "pkg-1", status: "available" });
    const calls = mocks.call.mock.calls;
    expect(calls.map(call => [call[0], call[1]])).toEqual([
      ["collector", "InitPackageUpload"],
      ["collector", "CompletePackageUpload"]
    ]);
    expect(calls[0][2]).toEqual({
      package_name: "moox-collector-plan",
      version: "v1.0.1",
      runtime: "Go1",
      filename: "moox-collector-v1.0.1.zip"
    });
    expect(calls[1][2]).toEqual({
      package_id: "pkg-1",
      file_size: file.size,
      file_md5: createHash("md5").update("zip-bytes").digest("hex")
    });
  });

  it("computes exact MD5 across empty and multi-chunk (>2 MiB) package files", async () => {
    const cases: Array<{ name: string; bytes: Uint8Array }> = [
      { name: "empty.zip", bytes: new Uint8Array(0) },
      { name: "single.zip", bytes: new Uint8Array(1024).map((_, i) => i & 0xff) },
      { name: "multi-chunk.zip", bytes: new Uint8Array(4 * 1024 * 1024 + 37).map((_, i) => (i * 17 + 11) & 0xff) }
    ];
    for (const item of cases) {
      mocks.call.mockReset().mockImplementation(async (_service: string, method: string) => {
        if (method === "InitPackageUpload") {
          return { package: { package_id: `pkg-${item.name}` }, upload_url: "https://cos.example/put" };
        }
        return { package: { package_id: `pkg-${item.name}`, status: "available" } };
      });
      vi.stubGlobal(
        "fetch",
        vi.fn(async () => ({ ok: true, status: 200 }))
      );
      const file = new File([item.bytes], item.name);
      await uploadFunctionPackage({ package_name: "moox-collector-plan", version: "v1.0.2", runtime: "Go1" }, file);
      const completeCall = mocks.call.mock.calls[1][2];
      expect(completeCall.file_size, item.name).toBe(item.bytes.length);
      expect(completeCall.file_md5, item.name).toBe(createHash("md5").update(item.bytes).digest("hex"));
    }
  });

  it("stops before PUT when Collector returns no upload address", async () => {
    mocks.call.mockReset().mockResolvedValue({ upload_url: "" });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      uploadFunctionPackage({ package_name: "p", version: "v1", runtime: "Go1" }, new File(["x"], "p.zip"))
    ).rejects.toThrow("没有返回上传地址");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("代码包列表与删除", () => {
  it("全局调用，不携带空间参数", async () => {
    mocks.call.mockReset().mockResolvedValue({ packages: [{ package_id: "pkg-1" }] });
    await expect(listFunctionPackages()).resolves.toEqual([{ package_id: "pkg-1" }]);
    await deleteFunctionPackage("pkg-1");
    const calls = mocks.call.mock.calls;
    expect(calls[0]).toEqual(["collector", "ListFunctionPackages", {}]);
    expect(calls[1]).toEqual(["collector", "DeleteFunctionPackage", { package_id: "pkg-1" }]);
  });

  it("状态与大小的文案", () => {
    expect(packageStatusLabel("pending")).toBe("待上传");
    expect(packageStatusLabel("available")).toBe("可用");
    expect(formatFileSize(2048)).toBe("2.0KB");
    expect(formatFileSize("5242880")).toBe("5.0MB");
  });
});
