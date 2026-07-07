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

const dataCollection = findDirectory('compute-collector');
const factorCompute = findDirectory('factor-compute');
const trading = findDirectory('trading');

assert(zhCN.includes('["compute-collector"]: "数据采集"'), 'compute-collector zh-CN label must be 数据采集');
assert(dataCollection.parentId === '0', 'compute-collector must be a root menu');
assert(factorCompute.parentId === '0', 'factor-compute must be moved to root');
assert(factorCompute.sort > dataCollection.sort, 'factor-compute must appear after data collection');
assert(factorCompute.sort < trading.sort, 'factor-compute must appear before trading');

console.log('menu structure ok');
