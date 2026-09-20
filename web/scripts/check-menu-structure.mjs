import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const staticMenu = fs.readFileSync(path.join(root, "src/api/modules/system/static-menu.ts"), "utf8");
const routes = fs.readFileSync(path.join(root, "src/router/route.ts"), "utf8");
const zhCN = fs.readFileSync(path.join(root, "src/lang/modules/zhCN.ts"), "utf8");
const collectorTaskWorkbench = fs.readFileSync(path.join(root, "src/views/collector/task-management/index.vue"), "utf8");
const collectorTaskResults = fs.readFileSync(path.join(root, "src/views/collector/task-results/index.vue"), "utf8");
const collectorTaskUI = [
  collectorTaskWorkbench,
  fs.readFileSync(path.join(root, "src/views/collector/collection-tasks/collection-tasks.vue"), "utf8"),
  fs.readFileSync(path.join(root, "src/views/collector/collection-tasks/resample-backfill.vue"), "utf8"),
  fs.readFileSync(path.join(root, "src/views/collector/task-instances/task-instances.vue"), "utf8"),
  fs.readFileSync(path.join(root, "src/views/home/home.vue"), "utf8"),
  collectorTaskResults
].join("\n");

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

function findDirectory(name) {
  const pattern = new RegExp(String.raw`directory\("([^"]+)",\s*"([^"]+)",\s*"([^"]+)",\s*"${name}",\s*"([^"]+)",\s*(\d+)`);
  const match = staticMenu.match(pattern);
  assert(match, `directory ${name} not found`);
  return {
    id: match[1],
    parentId: match[2],
    path: match[3],
    title: match[4],
    sort: Number(match[5])
  };
}

function findMenu(name) {
  const pattern = new RegExp(
    String.raw`menu\(\s*"([^"]+)",\s*"([^"]+)",\s*"([^"]+)",\s*"${name}",\s*"([^"]+)",\s*"([^"]+)",\s*(\d+)`
  );
  const match = staticMenu.match(pattern);
  assert(match, `menu ${name} not found`);
  return {
    id: match[1],
    parentId: match[2],
    path: match[3],
    title: match[4],
    component: match[5],
    sort: Number(match[6])
  };
}

function assertNotVisible(name) {
  assert(!staticMenu.includes(`"${name}", "${name}"`), `${name} must not be a visible static-menu entry`);
}

const dataCollection = findDirectory("compute-collector");
const factorCompute = findDirectory("factor-compute");
const trading = findDirectory("trading");
const ops = findDirectory("ops");

assert(zhCN.includes('["compute-collector"]: "数据采集"'), "compute-collector zh-CN label must be 数据采集");
assert(zhCN.includes('["factor-definitions"]: "因子定义"'), "factor-definitions zh-CN label must be 因子定义");
assert(zhCN.includes('["collector-tasks"]: "采集任务"'), "collector-tasks zh-CN label must be 采集任务");
assert(zhCN.includes('["data-fields"]: "基础字段"'), "data-fields zh-CN label must be 基础字段");
assert(zhCN.includes('["factor-datasets"]: "复合因子数据集"'), "factor-datasets zh-CN label must be 复合因子数据集");
assert(zhCN.includes('["factor-construct"]: "构造配置"'), "factor-construct zh-CN label must be 构造配置");
assert(zhCN.includes('["factor-tasks"]: "计算任务"'), "factor-tasks zh-CN label must be 计算任务");

assertNotVisible("data-assets");
assert(dataCollection.parentId === "0", "compute-collector must be a root menu");
assert(dataCollection.path === "/data/sources", "compute-collector default path must be /data/sources");
assert(factorCompute.parentId === "0", "factor-compute must be a root menu");
assert(factorCompute.sort > dataCollection.sort, "factor-compute must appear after data collection");
assert(factorCompute.sort < trading.sort, "factor-compute must appear before trading");
assert(ops.path === "/ops/hosts", "ops default path must be /ops/hosts");
const services = findMenu("ops-services");
const hosts = findMenu("ops-hosts");
assert(services.parentId === ops.id, "ops-services must be under ops");
assert(hosts.parentId === ops.id, "ops-hosts must be under ops");
assert(hosts.sort < services.sort, "host workbench must appear before service management");
assert(
  !staticMenu.includes('menu("0601", "06", "/ops/hosts", "ops-hosts", "ops-hosts", "ops/host-workbench/index", 1, {'),
  "ops-hosts must not have a custom icon"
);
assert(
  !staticMenu.includes(
    'menu("0600", "06", "/ops/services", "ops-services", "ops-services", "ops/service-management/index", 2, {'
  ),
  "ops-services must not have a custom icon"
);
assert(staticMenu.includes('svgIcon: "experiment"'), "factor icon must be unique");
assert(staticMenu.includes('svgIcon: "mind-mapping"'), "strategy icon must be unique");
assert(!staticMenu.includes('menu("0600", "06", "/ops/service-monitor"'), "legacy service monitor must not remain visible");
for (const retired of [
  "/settings/service-deployments",
  "/data/datasets",
  "/data/factors",
  "/data/views",
  "/data/view-browse",
  "/data/overview",
  "/data/list",
  "/data/browse",
  "/collector/functions",
  "/collector/datasets",
  "/collector/views",
  "/collector/packages",
  "/collector/data-management",
  "/ops/service-monitor",
  "/ops/metric-monitor",
  "/ops/resource-monitor",
  "/ops/ssh-hosts",
  "/ops/ssh-terminal",
  "/ops/ssh-sessions",
  "/ops/storage/archive"
]) {
  assert(!routes.includes(`path: "${retired}"`), `retired route ${retired} must be absent`);
}

const dataSources = findMenu("data-sources");
const dataSubjects = findMenu("data-subjects");
const dataFields = findMenu("data-fields");
assert(dataSources.parentId === dataCollection.id, "data-sources must be under data collection");
assert(dataSubjects.parentId === dataCollection.id, "data-subjects must be under data collection");
assert(dataFields.parentId === dataCollection.id, "data-fields must be under data collection");
assert(dataSources.sort < dataSubjects.sort, "data sources must appear before subjects");
assert(dataSubjects.sort < dataFields.sort, "subjects must appear before base fields");

const collectorTasks = findMenu("collector-tasks");
assert(collectorTasks.parentId === dataCollection.id, "collector-tasks must be under data collection");
assert(collectorTasks.path === "/collector/tasks", "collector-tasks path must be canonical");
assert(dataFields.sort < collectorTasks.sort, "base fields must appear before collection tasks");
assert(routes.includes('path: "/collector/tasks"'), "collector tasks route must exist");
const collectorTabOrder = ["采集任务", "任务实例", "执行器", "采集结果"].map(label =>
  collectorTaskWorkbench.indexOf(`label: "${label}"`)
);
assert(
  collectorTabOrder.every(position => position >= 0),
  "collector task workbench must expose all four tabs"
);
assert(
  collectorTabOrder.every((position, index) => index === 0 || position > collectorTabOrder[index - 1]),
  "collector task tabs must stay ordered"
);
assert(collectorTaskResults.includes("暂无采集任务"), "collector task results must expose the no-task state");
assert(collectorTaskResults.includes("结果准备中，请稍后刷新"), "collector task results must expose the pending state");
assert(collectorTaskResults.includes("resultTask"), "collector task results must persist the selected task");
assert(collectorTaskResults.includes(":view-ids="), "collector task results must scope the shared browser to the task View");
assert(!staticMenu.includes("collector-data-management"), "collector-data-management must not be visible");
assert(!staticMenu.includes("collector-rules"), "collector-rules must not be visible");
assert(!staticMenu.includes('menu("0304"'), "task instances must not remain a separate visible menu");
assert(!staticMenu.includes('menu("0302"'), "code packages must not remain a separate visible menu");
for (const hiddenLabel of ["数据集管理", "集合定义", "基础数据集"]) {
  assert(!collectorTaskUI.includes(hiddenLabel), `collector task UI must not expose ${hiddenLabel}`);
}
for (const hiddenLabel of ["规则正在回填", "按规则展开任务", "数据集"]) {
  assert(!collectorTaskUI.includes(hiddenLabel), `collector task UI must not expose ${hiddenLabel}`);
}

const factorDefinitions = findMenu("factor-definitions");
const factorDatasets = findMenu("factor-datasets");
const factorConstruct = findMenu("factor-construct");
const factorBindings = findMenu("factor-bindings");
const factorTasks = findMenu("factor-tasks");
assert(factorDefinitions.parentId === factorCompute.id, "factor-definitions must be under factor compute");
assert(factorDatasets.parentId === factorCompute.id, "factor-datasets must be under factor compute");
assert(factorConstruct.parentId === factorCompute.id, "factor-construct must be under factor compute");
assert(factorBindings.parentId === factorCompute.id, "factor-bindings must be under factor compute");
assert(factorTasks.parentId === factorCompute.id, "factor-tasks must be under factor compute");
assert(factorDefinitions.sort < factorDatasets.sort, "factor datasets must appear after definitions");
assert(factorDatasets.sort < factorConstruct.sort, "construct must appear after factor datasets");
assert(factorConstruct.sort < factorBindings.sort, "bindings must appear after construct");
assert(factorBindings.sort < factorTasks.sort, "tasks must appear after bindings");
assert(routes.includes('path: "/factor/datasets"'), "factor datasets route must exist");
assert(routes.includes('path: "/factor/construct"'), "factor construct route must exist");
assert(routes.includes('path: "/factor/tasks"'), "factor tasks route must exist");

assertNotVisible("data-modeling");
assertNotVisible("data-mgmt");
assertNotVisible("data-views");
assertNotVisible("data-factors");
assertNotVisible("data-overview");
assertNotVisible("data-browse");
assertNotVisible("data-view-list");
assertNotVisible("data-view-browse");
assertNotVisible("collector-datasets");
assertNotVisible("collector-views");
assertNotVisible("factor-results");

console.log("menu structure ok");
