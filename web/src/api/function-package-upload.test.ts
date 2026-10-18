import { afterEach, describe, expect, it, vi } from "vitest";
import { setSelectedSpaceIdCache } from "@/api/admin/space-header";

const mocks = vi.hoisted(() => ({ callControl: vi.fn() }));
vi.mock("@/api/admin/http", () => ({ callControl: mocks.callControl }));

import { uploadFunctionPackage } from "./function-package";

afterEach(() => {
  vi.unstubAllGlobals();
  setSelectedSpaceIdCache("");
});

describe("uploadFunctionPackage", () => {
  it("keeps the Space it started in for every step even if the user switches Space during the upload", async () => {
    setSelectedSpaceIdCache("space-a");
    mocks.callControl.mockReset().mockImplementation(async (_service: string, method: string) => {
      if (method === "InitPackageUpload") {
        return { package_id: "pkg-1", upload_url: "https://cos.example/put" };
      }
      return { detail: { package_id: "pkg-1" } };
    });
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => {
        setSelectedSpaceIdCache("space-b"); // PUT 期间用户切换了空间
        return { ok: true, status: 200 };
      })
    );
    const file = new File(["zip-bytes"], "pkg.zip");
    await uploadFunctionPackage(
      { package_name: "p", version: "1", runtime: "Go1", package_type: 1, cloud_account_id: "acc" } as never,
      file
    );
    const spaces = mocks.callControl.mock.calls.map(call => call[3]?.headers?.["X-Space-Id"]);
    expect(spaces).toEqual(["space-a", "space-a"]);
  });
});
