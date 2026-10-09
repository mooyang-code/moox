import fs from "node:fs";
import path from "node:path";

const root = path.resolve(path.dirname(new URL(import.meta.url).pathname), "..");
const read = file => fs.readFileSync(path.join(root, file), "utf8");
// 只检查产品代码；测试里会出现「不得包含」的词。
function readTree(dir) {
  return fs
    .readdirSync(dir, { withFileTypes: true })
    .filter(entry => entry.isDirectory() || !/\.test\.ts$/.test(entry.name))
    .map(entry => (entry.isDirectory() ? readTree(path.join(dir, entry.name)) : fs.readFileSync(path.join(dir, entry.name), "utf8")))
    .join("\n");
}
const all = readTree(path.join(root, "src"));
const page = [
  read("src/views/ops/monitor/index.vue"),
  read("src/views/ops/monitor/component-drawer.vue"),
  read("src/views/ops/monitor/notification-modal.vue"),
  read("src/views/ops/monitor/monitor-display.ts")
].join("\n");
const api = read("src/api/monitor/index.ts");

// 监控告警页：告警（含主机告警）、数据链路、组件 × 主机矩阵、主机、业务检查、未登记进程与推送设置。
for (const token of [
  "GetHealthOverview",
  "GetNotificationChannel",
  "UpdateNotificationChannel",
  "当前告警",
  "数据链路",
  "服务",
  "主机",
  "业务检查",
  "未登记的进程",
  "推送设置",
  "定位",
  "不探测",
  "原始错误"
]) {
  if (!page.includes(token) && !api.includes(token)) throw new Error(`monitor page contract missing ${token}`);
}
// 后端已经给出中文摘要，前端不再维护翻译表。
for (const token of ["displayConclusion", "conclusionTranslations", "health-monitor", "business_items", "service_items"]) {
  if (all.includes(token)) throw new Error(`retired health monitor code remains: ${token}`);
}
for (const token of [
  "CreateCheck",
  "UpdateCheck",
  "DeleteCheck",
  "RunCheckOnce",
  "CreateWebhookChannel",
  "DeleteWebhookChannel",
  "CreateAlertRule",
  "UpdateAlertRule",
  "DeleteAlertRule",
  "ListMetricServices",
  "QueryMetricHistory",
  "CreateMetricRule",
  "新增探测",
  "编辑探测",
  "手动运行",
  "新增告警规则",
  "原始指标名",
  "序列 ID",
  "Headers JSON",
  "Body Template"
]) {
  if (all.includes(token)) throw new Error(`retired monitoring capability remains: ${token}`);
}
console.log("monitor frontend contract passed");
