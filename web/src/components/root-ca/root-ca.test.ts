import { describe, expect, it, vi } from "vitest";
import {
  defaultGuideKey,
  fetchRootCaFingerprint,
  normalizeFingerprint,
  ROOT_CA_FILE_NAME,
  ROOT_CA_FINGERPRINT_URL,
  rootCaGuides
} from "./root-ca";

const FINGERPRINT = "55:A0:73:81:C9:0F:EB:18:5B:80:02:A8:59:39:7E:5E:BA:B5:50:2F:05:BF:27:C5:41:76:75:5A:6E:2B:2D:88";

describe("根证书指纹", () => {
  it("接受 32 字节冒号分隔的指纹，统一成大写并去掉空白", () => {
    expect(normalizeFingerprint(`${FINGERPRINT}\n`)).toBe(FINGERPRINT);
    expect(normalizeFingerprint(FINGERPRINT.toLowerCase())).toBe(FINGERPRINT);
  });

  it("拒绝长度不对、没有冒号或夹带其他内容的文本", () => {
    expect(normalizeFingerprint("")).toBeNull();
    expect(normalizeFingerprint(FINGERPRINT.replace(/:/g, ""))).toBeNull();
    expect(normalizeFingerprint(FINGERPRINT.slice(0, -3))).toBeNull();
    expect(normalizeFingerprint(`${FINGERPRINT}:00`)).toBeNull();
    expect(normalizeFingerprint("<html>not found</html>")).toBeNull();
  });

  it("从公开路径读取指纹", async () => {
    const fetcher = vi.fn().mockResolvedValue(new Response(`${FINGERPRINT}\n`, { status: 200 }));
    await expect(fetchRootCaFingerprint(fetcher as unknown as typeof fetch)).resolves.toBe(FINGERPRINT);
    expect(fetcher).toHaveBeenCalledWith(ROOT_CA_FINGERPRINT_URL, { cache: "no-store" });
  });

  it("没有开启内置 CA、返回非法内容或网络出错时返回 null 而不是抛错", async () => {
    const notFound = vi.fn().mockResolvedValue(new Response("root certificate unavailable", { status: 404 }));
    await expect(fetchRootCaFingerprint(notFound as unknown as typeof fetch)).resolves.toBeNull();
    const spa = vi.fn().mockResolvedValue(new Response("<!doctype html>", { status: 200 }));
    await expect(fetchRootCaFingerprint(spa as unknown as typeof fetch)).resolves.toBeNull();
    const broken = vi.fn().mockRejectedValue(new TypeError("Failed to fetch"));
    await expect(fetchRootCaFingerprint(broken as unknown as typeof fetch)).resolves.toBeNull();
  });
});

describe("各系统的安装说明", () => {
  it("覆盖 macOS、Linux、Windows 和 Firefox，命令都指向同一个证书文件名", () => {
    const guides = rootCaGuides();
    expect(guides.map(item => item.key)).toEqual(["macos", "linux", "windows", "ios", "android", "cli", "firefox"]);
    for (const guide of guides.filter(item => ["macos", "linux", "windows"].includes(item.key))) {
      expect(guide.commands.length).toBeGreaterThan(0);
      for (const command of guide.commands) expect(command).toContain(ROOT_CA_FILE_NAME);
    }
    expect(guides[0].commands.join("\n")).toContain("security add-trusted-cert");
    const firefox = guides.find(item => item.key === "firefox");
    expect(firefox?.commands).toEqual([]);
    expect(firefox?.note).toContain("security.enterprise_roots.enabled");
    const cli = guides.find(item => item.key === "cli");
    expect(cli?.commands.join("\n")).toContain("NODE_EXTRA_CA_CERTS");
    expect(cli?.commands.join("\n")).not.toMatch(/ -k|--insecure/);
  });
});

describe("手机的安装步骤", () => {
  it("iPhone：用 Safari 下载、安装描述文件，并且要在证书信任设置里打开完全信任", () => {
    const ios = rootCaGuides().find(item => item.key === "ios");
    const text = (ios?.steps || []).join("\n");
    expect(ios?.commands).toEqual([]);
    expect(text).toContain("Safari");
    expect(text).toContain("VPN 与设备管理");
    expect(text).toContain("证书信任设置");
    expect(text).toContain("SHA-256");
  });

  it("Android：安装为 CA 证书并核对指纹，提到 Firefox 的额外开关", () => {
    const android = rootCaGuides().find(item => item.key === "android");
    const text = (android?.steps || []).join("\n");
    expect(text).toContain("CA 证书");
    expect(text).toContain(ROOT_CA_FILE_NAME);
    expect(text).toContain("SHA-256");
    expect(text).toContain("第三方 CA 证书");
  });

  it("按设备自动选中页签", () => {
    expect(defaultGuideKey("Mozilla/5.0 (iPhone; CPU iPhone OS 18_0 like Mac OS X) Mobile/15E148 Safari/604.1")).toBe("ios");
    expect(
      defaultGuideKey(
        "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 Version/18.0 Mobile/15E148 Safari/604.1"
      )
    ).toBe("ios");
    expect(defaultGuideKey("Mozilla/5.0 (Linux; Android 14; Pixel 8) Chrome/126.0 Mobile Safari/537.36")).toBe("android");
    expect(defaultGuideKey("Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/126.0")).toBe("windows");
    expect(defaultGuideKey("Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) Chrome/126.0")).toBe("macos");
    expect(defaultGuideKey("Mozilla/5.0 (X11; Linux x86_64) Firefox/128.0")).toBe("linux");
    expect(defaultGuideKey("")).toBe("macos");
  });
});
