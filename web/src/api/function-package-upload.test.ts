import { afterEach, describe, expect, it, vi } from "vitest";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";

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
  setSelectedSpaceIdCache("");
});

describe("uploadFunctionPackage", () => {
  it("keeps the Space it started in for every step even if the user switches Space during the upload", async () => {
    setSelectedSpaceIdCache("space-a");
    mocks.call.mockReset().mockImplementation(async (_service: string, method: string) => {
      if (method === "InitPackageUpload") {
        return { package: { package_id: "pkg-1" }, upload_url: "https://cos.example/put" };
      }
      return { package: { package_id: "pkg-1", status: "available" } };
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        setSelectedSpaceIdCache("space-b"); // PUT 期间用户切换了空间
        return { ok: true, status: 200 };
      })
    );
    const file = new File(["zip-bytes"], "moox-collector-v1.0.1.zip");
    const result = await uploadFunctionPackage(
      { package_name: "moox-collector", version: "v1.0.1", runtime: "Go1", cloud_account_id: "acc" },
      file
    );
    expect(result).toMatchObject({ package_id: "pkg-1", status: "available" });
    const calls = mocks.call.mock.calls;
    expect(calls.map(call => [call[0], call[1]])).toEqual([
      ["collector", "InitPackageUpload"],
      ["collector", "CompletePackageUpload"]
    ]);
    // 请求头与请求体里的空间是同一个值，都是发起时的空间
    expect(calls.map(call => call[3]?.spaceId)).toEqual(["space-a", "space-a"]);
    expect(calls.map(call => call[2].space_id)).toEqual(["space-a", "space-a"]);
    expect(calls[0][2]).toMatchObject({ filename: "moox-collector-v1.0.1.zip", cloud_account_id: "acc" });
    expect(calls[1][2]).toMatchObject({ package_id: "pkg-1", file_size: file.size });
    expect(calls[1][2].file_md5).toMatch(/^[0-9a-f]{32}$/);
  });

  it("stops before PUT when Collector returns no upload address", async () => {
    mocks.call.mockReset().mockResolvedValue({ upload_url: "" });
    const fetchMock = vi.fn();
    vi.stubGlobal("fetch", fetchMock);
    await expect(
      uploadFunctionPackage(
        { package_name: "p", version: "v1", runtime: "Go1", cloud_account_id: "acc" },
        new File(["x"], "p.zip"),
        "s"
      )
    ).rejects.toThrow("没有返回上传地址");
    expect(fetchMock).not.toHaveBeenCalled();
  });
});

describe("代码包列表与删除", () => {
  it("请求体和请求头都带同一个空间", async () => {
    mocks.call.mockReset().mockResolvedValue({ packages: [{ package_id: "pkg-1" }] });
    await expect(listFunctionPackages("crypto")).resolves.toEqual([{ package_id: "pkg-1" }]);
    await deleteFunctionPackage("crypto", "pkg-1");
    const calls = mocks.call.mock.calls;
    expect(calls[0].slice(0, 3)).toEqual(["collector", "ListFunctionPackages", { space_id: "crypto" }]);
    expect(calls[1].slice(0, 3)).toEqual(["collector", "DeleteFunctionPackage", { space_id: "crypto", package_id: "pkg-1" }]);
    expect(calls.map(call => call[3]?.spaceId)).toEqual(["crypto", "crypto"]);
  });

  it("状态与大小的文案", () => {
    expect(packageStatusLabel("pending")).toBe("待上传");
    expect(packageStatusLabel("available")).toBe("可用");
    expect(formatFileSize(2048)).toBe("2.0KB");
    expect(formatFileSize("5242880")).toBe("5.0MB");
  });
});
