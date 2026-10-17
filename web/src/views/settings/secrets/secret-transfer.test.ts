import { describe, expect, it } from "vitest";
import { exportFileName, importSummary, passphraseError } from "./secret-transfer";

describe("secret transfer helpers", () => {
  it("requires a long enough passphrase typed twice, counting Chinese characters", () => {
    expect(passphraseError("short", "short")).toBe("导出口令至少 12 个字符");
    expect(passphraseError("正好十二个字符的迁移口令啊", "正好十二个字符的迁移口令啊")).toBe("");
    expect(passphraseError("correct-horse-battery", "correct-horse-batterz")).toBe("两次输入的口令不一致");
  });

  it("names the bundle by local export time", () => {
    expect(exportFileName(new Date(2026, 9, 16, 8, 5, 3))).toBe("moox-secrets-20261016-080503.json");
  });

  it("summarizes the import result", () => {
    expect(importSummary({ created: 3 })).toBe("新增 3 个，覆盖 0 个");
    expect(importSummary({ created: 1, updated: 2, skipped: ["A"], failed: ["B：分类无效"] })).toBe(
      "新增 1 个，覆盖 2 个，已存在跳过 1 个，失败 1 个"
    );
  });
});
