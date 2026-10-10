import fs from "node:fs";
import path from "node:path";
import process from "node:process";

const root = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..");
const read = file => fs.readFileSync(path.join(root, file), "utf8");
function readTree(dir) { return fs.readdirSync(dir, { withFileTypes: true }).map(entry => entry.isDirectory() ? readTree(path.join(dir, entry.name)) : fs.readFileSync(path.join(dir, entry.name), "utf8")).join("\n"); }
const all = readTree(path.join(root, "src"));
const page = read("src/views/ops/monitor/index.vue");
const api = read("src/api/monitor/index.ts");
for (const token of ["GetHealthOverview", "GetNotificationChannel", "UpdateNotificationChannel", "当前告警", "组件", "数据链路", "业务检查", "主机", "未登记进程", "topology_known", "summary", "RawError"]) {
  if (!page.includes(token) && !api.includes(token)) throw new Error(`health monitor contract missing ${token}`);
}
for (const token of ["CreateCheck", "UpdateCheck", "DeleteCheck", "RunCheckOnce", "CreateWebhookChannel", "DeleteWebhookChannel", "CreateAlertRule", "UpdateAlertRule", "DeleteAlertRule", "ListMetricServices", "QueryMetricHistory", "CreateMetricRule", "新增探测", "编辑探测", "手动运行", "新增告警规则", "原始指标名", "序列 ID", "Headers JSON", "Body Template"]) {
  if (all.includes(token)) throw new Error(`retired monitoring capability remains: ${token}`);
}
for (const token of ["displayConclusion", "business_items", "service_items"]) {
  if (page.includes(token)) throw new Error(`retired health model remains: ${token}`);
}
console.log("health monitor frontend contract passed");
