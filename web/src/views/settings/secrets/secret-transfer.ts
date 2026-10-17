import type { ImportSecretsRsp } from "@/api/admin/secret";

/** 导出口令的最少字符数，与服务端一致：迁移包离开系统后只靠口令保护。 */
export const MIN_PASSPHRASE_LENGTH = 12;

/** 校验导出口令与确认口令，返回错误提示；通过时返回空串。按字符（含中文）计数。 */
export function passphraseError(passphrase: string, confirm: string): string {
  if ([...passphrase].length < MIN_PASSPHRASE_LENGTH) return `导出口令至少 ${MIN_PASSPHRASE_LENGTH} 个字符`;
  if (passphrase !== confirm) return "两次输入的口令不一致";
  return "";
}

/** 迁移包的文件名：moox-secrets-年月日-时分秒.json（本地时间）。 */
export function exportFileName(now: Date): string {
  const pad = (value: number) => String(value).padStart(2, "0");
  const day = `${now.getFullYear()}${pad(now.getMonth() + 1)}${pad(now.getDate())}`;
  const time = `${pad(now.getHours())}${pad(now.getMinutes())}${pad(now.getSeconds())}`;
  return `moox-secrets-${day}-${time}.json`;
}

/** 导入结果的一句话摘要。 */
export function importSummary(rsp: ImportSecretsRsp): string {
  const parts = [`新增 ${rsp.created || 0} 个`, `覆盖 ${rsp.updated || 0} 个`];
  if (rsp.skipped?.length) parts.push(`已存在跳过 ${rsp.skipped.length} 个`);
  if (rsp.failed?.length) parts.push(`失败 ${rsp.failed.length} 个`);
  return parts.join("，");
}

/** 把文本保存为文件（浏览器下载）。 */
export function saveTextFile(name: string, text: string) {
  const url = URL.createObjectURL(new Blob([text], { type: "application/json" }));
  const link = document.createElement("a");
  link.href = url;
  link.download = name;
  document.body.appendChild(link);
  link.click();
  link.remove();
  URL.revokeObjectURL(url);
}
