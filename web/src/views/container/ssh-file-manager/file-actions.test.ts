import { describe, expect, it } from "vitest";
import { deleteConfirmText } from "./file-actions";

describe("SFTP 删除的确认说明", () => {
  it("目录写明连同全部内容，文件写完整路径，都带主机", () => {
    expect(deleteConfirmText({ type: "d", path: "/home/ubuntu/data" }, "控制机")).toBe(
      "在 控制机 删除目录 /home/ubuntu/data 及其全部内容？此操作不可恢复。"
    );
    expect(deleteConfirmText({ type: "f", path: "/tmp/a.log" }, "")).toBe("删除文件 /tmp/a.log？此操作不可恢复。");
  });
});
