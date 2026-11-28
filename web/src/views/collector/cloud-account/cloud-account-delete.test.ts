import { describe, expect, it } from "vitest";
import { CLOUD_ACCOUNT_DELETE_TEXT, cloudAccountDeleteError } from "./cloud-account-delete";

describe("删除云账号", () => {
  it("确认正文写明只删登记，以及旧账号下的函数怎么找出来删", () => {
    expect(CLOUD_ACCOUNT_DELETE_TEXT).toContain("只删除这条登记，不会删除云上的函数");
    expect(CLOUD_ACCOUNT_DELETE_TEXT).toContain("Collector 会拒绝并说明原因");
    expect(CLOUD_ACCOUNT_DELETE_TEXT).toContain("moox-cli collector function delete 也删不到");
    expect(CLOUD_ACCOUNT_DELETE_TEXT).toContain("moox-cli collector function inspect");
    expect(CLOUD_ACCOUNT_DELETE_TEXT).toContain("腾讯云控制台手工删除");
  });

  it("被 Collector 拒绝时显示它给的原因", () => {
    const refused = new Error("云账号 acc-1 还有 2 个代码包");
    expect(cloudAccountDeleteError(refused)).toBe("删除云账号失败：云账号 acc-1 还有 2 个代码包");
    expect(cloudAccountDeleteError(null)).toBe("删除云账号失败：未知原因");
  });
});
