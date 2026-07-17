import type { HostMetrics } from "@/api/modules/host-monitor";
import type { SSHHost, SessionInfo } from "@/api/modules/ssh";

export interface HostWorkbenchRow {
  key: string;
  monitor?: HostMetrics;
  ssh?: SSHHost;
  sessions: SessionInfo[];
  match: "address" | "unique_name" | "unmatched";
}

function normalize(value?: string) {
  return (value || "").trim().toLowerCase();
}

export function mergeHostWorkbenchRows(
  monitors: HostMetrics[],
  sshHosts: SSHHost[],
  sessions: SessionInfo[] = []
): HostWorkbenchRow[] {
  const rows: HostWorkbenchRow[] = [];
  const usedSSH = new Set<number>();
  const sessionsByHost = new Map<number, SessionInfo[]>();
  for (const session of sessions) {
    const current = sessionsByHost.get(session.host_id) || [];
    current.push(session);
    sessionsByHost.set(session.host_id, current);
  }

  for (const monitor of monitors) {
    const address = normalize(monitor.address || monitor.host_id);
    const exact = sshHosts.filter(host => host.id !== undefined && !usedSSH.has(host.id) && normalize(host.address) === address);
    let ssh = exact.length === 1 ? exact[0] : undefined;
    let match: HostWorkbenchRow["match"] = ssh ? "address" : "unmatched";
    if (!ssh) {
      const sameName = sshHosts.filter(
        host => host.id !== undefined && !usedSSH.has(host.id) && normalize(host.name) === normalize(monitor.host_name)
      );
      if (sameName.length === 1) {
        ssh = sameName[0];
        match = "unique_name";
      }
    }
    if (ssh?.id !== undefined) usedSSH.add(ssh.id);
    rows.push({
      key: `monitor:${monitor.host_id}`,
      monitor,
      ssh,
      sessions: ssh?.id !== undefined ? sessionsByHost.get(ssh.id) || [] : [],
      match
    });
  }

  for (const host of sshHosts) {
    if (host.id !== undefined && usedSSH.has(host.id)) continue;
    rows.push({
      key: `ssh:${host.id}`,
      ssh: host,
      sessions: host.id !== undefined ? sessionsByHost.get(host.id) || [] : [],
      match: "unmatched"
    });
  }
  return rows;
}
