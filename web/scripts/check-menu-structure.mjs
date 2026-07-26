import fs from "node:fs";
import path from "node:path";
import { fileURLToPath } from "node:url";

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, "..");
const staticMenu = fs.readFileSync(path.join(root, "src/api/modules/system/static-menu.ts"), "utf8");
const routes = fs.readFileSync(path.join(root, "src/router/route.ts"), "utf8");
const zhCN = fs.readFileSync(path.join(root, "src/lang/modules/zhCN.ts"), "utf8");

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

const dataAssets = findDirectory("data-assets");
const dataCollection = findDirectory("compute-collector");
const factorCompute = findDirectory("factor-compute");
const trading = findDirectory("trading");
const ops = findDirectory("ops");

assert(zhCN.includes('["compute-collector"]: "数据采集"'), "compute-collector zh-CN label must be 数据采集");
assert(zhCN.includes('["factor-definitions"]: "因子定义"'), "factor-definitions zh-CN label must be 因子定义");
assert(zhCN.includes('["collector-data-management"]: "数据管理"'), "collector-data-management zh-CN label must be 数据管理");
assert(zhCN.includes('["collector-datasets"]: "数据集合"'), "collector-datasets zh-CN label must be 数据集合");
assert(zhCN.includes('["collector-views"]: "数据视图"'), "collector-views zh-CN label must be 数据视图");
assert(zhCN.includes('["factor-results"]: "因子结果"'), "factor-results zh-CN label must be 因子结果");

assert(dataAssets.parentId === "0", "data-assets must remain a root menu");
assert(dataAssets.path === "/data/sources", "data-assets default path must be /data/sources");
assert(dataCollection.parentId === "0", "compute-collector must be a root menu");
assert(dataCollection.path === "/collector/data-management", "compute-collector default path must be /collector/data-management");
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
for (const path of [
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
  "/collector/tasks",
  "/ops/service-monitor",
  "/ops/metric-monitor",
  "/ops/resource-monitor",
  "/ops/ssh-hosts",
  "/ops/ssh-terminal",
  "/ops/ssh-sessions",
  "/ops/storage/archive"
]) {
  assert(!routes.includes(`path: "${path}"`), `retired route ${path} must be absent`);
}

const dataSources = findMenu("data-sources");
const dataSubjects = findMenu("data-subjects");
const dataFields = findMenu("data-fields");
assert(dataSources.parentId === dataAssets.id, "data-sources must be under data-assets");
assert(dataSubjects.parentId === dataAssets.id, "data-subjects must be under data-assets");
assert(dataFields.parentId === dataAssets.id, "data-fields must be under data-assets");

const collectorDataManagement = findMenu("collector-data-management");
const collectorRules = findMenu("collector-rules");
const collectorCloudnodes = findMenu("collector-cloudnodes");
assert(collectorDataManagement.parentId === dataCollection.id, "collector-data-management must be under data collection");
assert(collectorDataManagement.path === "/collector/data-management", "collector-data-management path must be canonical");
assert(collectorRules.parentId === dataCollection.id, "collector-rules must be under data collection");
assert(collectorCloudnodes.parentId === dataCollection.id, "collector-cloudnodes must be under data collection");
assert(collectorDataManagement.sort < collectorRules.sort, "data management must appear before collection rules");
assert(!staticMenu.includes('menu("0304"'), "task instances must not remain a separate visible menu");
assert(!staticMenu.includes('menu("0302"'), "code packages must not remain a separate visible menu");

const factorDefinitions = findMenu("factor-definitions");
const factorBindings = findMenu("factor-bindings");
const factorResults = findMenu("factor-results");
assert(factorDefinitions.parentId === factorCompute.id, "factor-definitions must be under factor compute");
assert(factorBindings.parentId === factorCompute.id, "factor-bindings must be under factor compute");
assert(factorResults.parentId === factorCompute.id, "factor-results must be under factor compute");
assert(factorBindings.sort < factorResults.sort, "factor results must appear after factor bindings");

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

console.log("menu structure ok");
