import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const root = path.resolve(scriptDir, '..');
const staticMenu = fs.readFileSync(path.join(root, 'src/api/modules/system/static-menu.ts'), 'utf8');
const zhCN = fs.readFileSync(path.join(root, 'src/lang/modules/zhCN.ts'), 'utf8');

function assert(condition, message) {
  if (!condition) {
    throw new Error(message);
  }
}

function findDirectory(name) {
  const pattern = new RegExp(
    String.raw`directory\("([^"]+)",\s*"([^"]+)",\s*"([^"]+)",\s*"${name}",\s*"([^"]+)",\s*(\d+)`,
  );
  const match = staticMenu.match(pattern);
  assert(match, `directory ${name} not found`);
  return {
    id: match[1],
    parentId: match[2],
    path: match[3],
    title: match[4],
    sort: Number(match[5]),
  };
}

function findMenu(name) {
  const pattern = new RegExp(
    String.raw`menu\("([^"]+)",\s*"([^"]+)",\s*"([^"]+)",\s*"${name}",\s*"([^"]+)",\s*"([^"]+)",\s*(\d+)`,
  );
  const match = staticMenu.match(pattern);
  assert(match, `menu ${name} not found`);
  return {
    id: match[1],
    parentId: match[2],
    path: match[3],
    title: match[4],
    component: match[5],
    sort: Number(match[6]),
  };
}

function assertNotVisible(name) {
  assert(!staticMenu.includes(`"${name}", "${name}"`), `${name} must not be a visible static-menu entry`);
}

const dataAssets = findDirectory('data-assets');
const dataCollection = findDirectory('compute-collector');
const factorCompute = findDirectory('factor-compute');
const trading = findDirectory('trading');
const ops = findDirectory('ops');

assert(zhCN.includes('["compute-collector"]: "数据采集"'), 'compute-collector zh-CN label must be 数据采集');
assert(zhCN.includes('["factor-definitions"]: "因子定义"'), 'factor-definitions zh-CN label must be 因子定义');
assert(zhCN.includes('["collector-datasets"]: "数据集合"'), 'collector-datasets zh-CN label must be 数据集合');
assert(zhCN.includes('["collector-views"]: "数据视图"'), 'collector-views zh-CN label must be 数据视图');
assert(zhCN.includes('["factor-results"]: "因子结果"'), 'factor-results zh-CN label must be 因子结果');

assert(dataAssets.parentId === '0', 'data-assets must remain a root menu');
assert(dataAssets.path === '/data/sources', 'data-assets default path must be /data/sources');
assert(dataCollection.parentId === '0', 'compute-collector must be a root menu');
assert(dataCollection.path === '/collector/datasets', 'compute-collector default path must be /collector/datasets');
assert(factorCompute.parentId === '0', 'factor-compute must be a root menu');
assert(factorCompute.sort > dataCollection.sort, 'factor-compute must appear after data collection');
assert(factorCompute.sort < trading.sort, 'factor-compute must appear before trading');
assert(ops.path === '/ops/hosts', 'ops default path must be /ops/hosts');
const services = findMenu('ops-services');
const hosts = findMenu('ops-hosts');
assert(services.parentId === ops.id, 'ops-services must be under ops');
assert(hosts.parentId === ops.id, 'ops-hosts must be under ops');
assert(hosts.sort < services.sort, 'host workbench must appear before service management');
assert(staticMenu.includes('svgIcon: "experiment"'), 'factor icon must be unique');
assert(staticMenu.includes('svgIcon: "mind-mapping"'), 'strategy icon must be unique');
assert(!staticMenu.includes('menu("0600", "06", "/ops/service-monitor"'), 'legacy service monitor must not remain visible');

const dataSources = findMenu('data-sources');
const dataSubjects = findMenu('data-subjects');
const dataFields = findMenu('data-fields');
assert(dataSources.parentId === dataAssets.id, 'data-sources must be under data-assets');
assert(dataSubjects.parentId === dataAssets.id, 'data-subjects must be under data-assets');
assert(dataFields.parentId === dataAssets.id, 'data-fields must be under data-assets');

const collectorDatasets = findMenu('collector-datasets');
const collectorViews = findMenu('collector-views');
const collectorRules = findMenu('collector-rules');
const collectorTasks = findMenu('collector-tasks');
const collectorCloudnodes = findMenu('collector-cloudnodes');
const collectorPackages = findMenu('collector-packages');
assert(collectorDatasets.parentId === dataCollection.id, 'collector-datasets must be under data collection');
assert(collectorViews.parentId === dataCollection.id, 'collector-views must be under data collection');
assert(collectorRules.parentId === dataCollection.id, 'collector-rules must be under data collection');
assert(collectorTasks.parentId === dataCollection.id, 'collector-tasks must be under data collection');
assert(collectorCloudnodes.parentId === dataCollection.id, 'collector-cloudnodes must be under data collection');
assert(collectorPackages.parentId === dataCollection.id, 'collector-packages must be under data collection');
assert(collectorDatasets.sort < collectorViews.sort, 'data collections must appear before data views');
assert(collectorViews.sort < collectorRules.sort, 'data views must appear before collection rules');

const factorDefinitions = findMenu('factor-definitions');
const factorBindings = findMenu('factor-bindings');
const factorResults = findMenu('factor-results');
assert(factorDefinitions.parentId === factorCompute.id, 'factor-definitions must be under factor compute');
assert(factorBindings.parentId === factorCompute.id, 'factor-bindings must be under factor compute');
assert(factorResults.parentId === factorCompute.id, 'factor-results must be under factor compute');
assert(factorBindings.sort < factorResults.sort, 'factor results must appear after factor bindings');

assertNotVisible('data-modeling');
assertNotVisible('data-mgmt');
assertNotVisible('data-views');
assertNotVisible('data-factors');
assertNotVisible('data-overview');
assertNotVisible('data-browse');
assertNotVisible('data-view-list');
assertNotVisible('data-view-browse');

console.log('menu structure ok');
