import { readFileSync, readdirSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";

const root = resolve(__dirname, "..");
const normalizeSource = (value: string) => value.replace(/\s+/g, "").replace(/'/g, '"');
function sourceFiles(directory: string): string[] {
  return readdirSync(directory, { withFileTypes: true }).flatMap(entry => {
    const path = resolve(directory, entry.name);
    if (entry.isDirectory()) return sourceFiles(path);
    return /\.(ts|vue)$/.test(entry.name) && !entry.name.endsWith(".test.ts") ? [path] : [];
  });
}

const source = sourceFiles(resolve(root, "src"))
  .map(file => readFileSync(file, "utf8"))
  .join("\n");

describe("frontend network contract", () => {
  it("does not construct service bypass or direct Admin gateway URLs", () => {
    expect(source).not.toMatch(/\/api\/service\//);
    expect(source).not.toMatch(/11000|VITE_GATEWAY_PORT|DEFAULT_GATEWAY_PORT/);
  });

  it("keeps auth and raw routes under the same-origin Admin gateway", () => {
    expect(normalizeSource(source)).toContain('call<{username:string;password:string},LoginResult>("auth","Login"');
    expect(source).toContain("/api/admin/ssh/WsConnect");
    expect(source).toContain("/api/admin/ssh/SftpDownload");
    expect(source).toContain("/api/admin/ssh/SftpUpload");
    // 归档的两个裸路由用模板字符串拼方法名：核对前缀与两个方法名都在同一个文件里
    const archiveRaw = readFileSync(resolve(root, "src/api/archive/raw.ts"), "utf8");
    expect(archiveRaw).toContain("/api/admin/archive/${method}");
    expect(archiveRaw).toContain('"PartitionDownload"');
    expect(archiveRaw).toContain('"RecentRows"');
  });

  // 会话在 HttpOnly Cookie 里：页面不再取盐值、加密密码、签名请求、换票据，也不在本地存令牌。
  it("relies on the session cookie instead of tokens, signatures or tickets", () => {
    expect(source).not.toMatch(/GetLoginSalt|IssueRawSessionTicket|request-signing|X-Moox-Signature|ticket=/);
    expect(source).not.toMatch(/Authorization|X-Access-Token|access_token|request_signing_key/);
    expect(source).not.toMatch(/VITE_ADMIN_ORIGIN|withCredentials:\s*true/);
  });

  it("uploads to external pre-signed URLs without session credentials", () => {
    const uploadSource = readFileSync(resolve(root, "src/api/function-package.ts"), "utf8");
    expect(normalizeSource(uploadSource)).toContain('fetch(initRsp.upload_url,{method:"PUT",body:file})');
    expect(uploadSource).not.toMatch(/upload_url[\s\S]{0,200}(Authorization|X-Access-Token|X-Moox-|X-Moox-Space)/);
  });
});
