import fs from "node:fs";
import path from "node:path";
const read = file => fs.readFileSync(path.join(process.cwd(), file), "utf8");
const source = read("src/views/ops/deployments/index.vue");
const api = read("src/api/admin/sysdeploy.ts");
for (const token of ["PageTitleTabs", 'aria-label="服务部署"', "网关路由", "受保护", "显示已停用", "上报实例", "compiled_at", "routeSync", "moox skill"]) {
  if (!source.includes(token)) throw new Error(`deployment page missing ${token}`);
}
for (const method of ["GetCatalog", "ListHosts", "ListDeployments", "GetHostRoutes", "GetDirectory"]) {
  if (!api.includes(`"${method}"`)) throw new Error(`deployment API missing ${method}`);
}
// 机器和部署的变更只通过 moox skill，控制台不再调用写接口。
for (const retired of ["SetHostStatus", "SetDeploymentStatus", "ListServiceDeployments", "CreateServiceDeployment", "UpdateServiceDeployment", "DeleteServiceDeployment", "ListGatewayNodes", "CreateGatewayNode", "GetGatewayNodeRoutes"]) {
  if (api.includes(retired)) throw new Error(`retired API remains: ${retired}`);
  if (source.includes(retired)) throw new Error(`retired API used by page: ${retired}`);
}
for (const file of ["src/views/ops/service-management", "src/views/settings/service-deployments"]) {
  if (fs.existsSync(path.join(process.cwd(), file))) throw new Error(`retired page remains: ${file}`);
}
const topology = read("src/views/ops/deployments/topology.ts");
if (!topology.includes('to: "factor-engine"') || topology.includes("ext:factor-engine")) {
  throw new Error("拓扑数据流的因子步骤必须引用已部署的 factor-engine 组件，而不是外部节点");
}
console.log("deployment frontend contract passed");
