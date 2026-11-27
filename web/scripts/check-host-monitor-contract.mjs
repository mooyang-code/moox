import fs from "node:fs";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");

const files = [
  "src/api/modules/host-monitor.ts",
  "src/views/ops/host-workbench/index.vue",
  "src/views/ops/host-workbench/host-monitor.vue",
  "src/views/ops/host-workbench/host-monitor-card-grid.vue",
  "src/views/ops/host-workbench/host-monitor-master-detail.vue",
  "src/views/ops/host-workbench/host-monitor-detail.vue",
  "src/router/route.ts",
  "src/views/ops/host-workbench/ssh-hosts.vue"
];

const sources = files
  .map(relativePath => {
    const abs = path.join(root, relativePath);
    return fs.existsSync(abs) ? fs.readFileSync(abs, "utf8") : "";
  })
  .join("\n");

// 核心契约：主机监控基于 SysDeploy 已登记主机与指标可用性标志，不恢复旧 SSH 主机清单或在线会话页。
const requiredContracts = [
  "listAllDeploymentHosts",
  "getCurrentMetrics",
  "buildHostMonitorRows",
  "sshAvailability",
  "maxAvailableFilesystemUsage",
  "aggregateNetworkRate",
  "historyHasRenderableData",
  "PageTitleTabs"
];

const missing = requiredContracts.filter(token => !sources.includes(token));
if (missing.length) {
  console.error(`host monitor contract missing: ${missing.join(", ")}`);
  process.exit(1);
}

const retiredTokens = [
  "listSSHHosts",
  "createSSHHost",
  "updateSSHHost",
  "deleteSSHHost",
  "getSSHHostDetail",
  "mergeHostWorkbenchRows",
  "deployment_host_id",
  "unique_name",
  "monitorOnly",
  "SshSessions",
  "getOnlineSessions",
  'tab: "sessions"'
];

const remaining = retiredTokens.filter(token => sources.includes(token));
if (remaining.length) {
  console.error(`retired host monitor contract still present: ${remaining.join(", ")}`);
  process.exit(1);
}

console.log("host monitor frontend contract passed");
