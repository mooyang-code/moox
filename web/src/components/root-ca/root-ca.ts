// 控制台代理在内置 CA 模式下公开的根证书与指纹（登录前可访问），换了主机的用户靠它安装信任。
export const ROOT_CA_CERT_URL = "/ca/root.crt";
export const ROOT_CA_FINGERPRINT_URL = "/ca/root.sha256";
/** 安装包：根证书、指纹、给 Agent 阅读的安装说明（AGENT-INSTALL.md）和各系统的安装脚本。 */
export const ROOT_CA_BUNDLE_URL = "/ca/moox-root-ca.zip";
export const ROOT_CA_BUNDLE_FILE_NAME = "moox-root-ca.zip";
export const ROOT_CA_FILE_NAME = "moox-caddy-root.crt";

export interface RootCaGuide {
  key: "macos" | "linux" | "windows" | "firefox" | "cli";
  label: string;
  /** 每一项是一条可直接复制执行的命令；没有命令时只展示 note。 */
  commands: string[];
  note?: string;
}

const FINGERPRINT_PATTERN = /^[0-9A-F]{2}(:[0-9A-F]{2}){31}$/;

/** 校验并规范化服务端返回的 SHA-256 指纹（32 字节、冒号分隔、大写十六进制），格式不对返回 null。 */
export function normalizeFingerprint(text: string): string | null {
  const value = text.trim().toUpperCase();
  return FINGERPRINT_PATTERN.test(value) ? value : null;
}

/** 读取根证书指纹；没有开启内置 CA、页面由开发服务器提供、网络出错时返回 null，不抛错。 */
export async function fetchRootCaFingerprint(fetcher: typeof fetch = fetch): Promise<string | null> {
  try {
    const response = await fetcher(ROOT_CA_FINGERPRINT_URL, { cache: "no-store" });
    if (!response.ok) return null;
    return normalizeFingerprint(await response.text());
  } catch {
    return null;
  }
}

/** 各系统安装根证书的命令；证书文件默认放在“下载”目录。 */
export function rootCaGuides(fileName: string = ROOT_CA_FILE_NAME): RootCaGuide[] {
  return [
    {
      key: "macos",
      label: "macOS",
      commands: [
        `[ "$(grep -c '^-----BEGIN ' ~/Downloads/${fileName})" = 1 ] && openssl x509 -in ~/Downloads/${fileName} -noout -fingerprint -sha256`,
        `sudo security add-trusted-cert -d -r trustRoot -p ssl -k /Library/Keychains/System.keychain ~/Downloads/${fileName}`
      ],
      note: "第一条确认文件里只有一张证书并核对指纹，第二条安装；会要求输入本机密码。装好后彻底退出并重新打开浏览器。"
    },
    {
      key: "linux",
      label: "Linux",
      commands: [
        `[ "$(grep -c '^-----BEGIN ' ~/Downloads/${fileName})" = 1 ] && openssl x509 -in ~/Downloads/${fileName} -noout -fingerprint -sha256`,
        `sudo cp ~/Downloads/${fileName} /usr/local/share/ca-certificates/${fileName} && sudo update-ca-certificates`,
        `sudo cp ~/Downloads/${fileName} /etc/pki/ca-trust/source/anchors/${fileName} && sudo update-ca-trust`
      ],
      note: "第二条用于 Debian / Ubuntu，第三条用于 RHEL / Fedora。Chrome 在 Linux 上用自己的证书库时，也要在浏览器设置里导入。"
    },
    {
      key: "windows",
      label: "Windows",
      commands: [
        `find /c "-----BEGIN " "%USERPROFILE%\\Downloads\\${fileName}"`,
        `certutil -dump "%USERPROFILE%\\Downloads\\${fileName}"`,
        `certutil -addstore -f Root "%USERPROFILE%\\Downloads\\${fileName}"`
      ],
      note: "在管理员命令提示符中执行；第一条的输出里有 Cert Hash(sha256) 可核对指纹，第二条安装到“受信任的根证书颁发机构”。"
    },
    {
      key: "cli",
      label: "命令行 / 脚本",
      commands: [
        `curl --cacert ~/Downloads/${fileName} https://<管理台地址>/`,
        `export NODE_EXTRA_CA_CERTS=~/Downloads/${fileName}`,
        `export REQUESTS_CA_BUNDLE=~/Downloads/${fileName}`
      ],
      note: "装进系统信任库后，系统自带的 curl 和 Go 程序直接可用；Node.js、Python、Homebrew 的 curl 不读系统信任库，要用上面的方式指定根证书。不要使用 curl -k 等关闭校验的开关。"
    },
    {
      key: "firefox",
      label: "Firefox",
      commands: [],
      note: "Firefox 使用自己的证书库：设置 → 隐私与安全 → 证书 → 查看证书 → 证书颁发机构 → 导入，并勾选“信任由此证书颁发机构来标识网站”；也可以在 about:config 把 security.enterprise_roots.enabled 设为 true，改读系统证书库。"
    }
  ];
}
