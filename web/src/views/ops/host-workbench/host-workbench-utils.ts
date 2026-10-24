import type { DeploymentHost } from "@/api/admin/types";

export interface SshAvailability {
  ok: boolean;
  /** 不可用时给用户看的原因 */
  reason: string;
}

// 只有登记了 SSH 端口的主机才能从控制台打开终端和文件管理。
export function sshAvailability(host: DeploymentHost): SshAvailability {
  if ((host.ssh_port ?? 0) > 0) return { ok: true, reason: "" };
  return { ok: false, reason: "这台主机没有登记 SSH，请通过 moox skill 登记后再连接" };
}
