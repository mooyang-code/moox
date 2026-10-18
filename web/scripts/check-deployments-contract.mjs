import fs from "node:fs";
import path from "node:path";
const read = file => fs.readFileSync(path.join(process.cwd(), file), "utf8");
const source = read("src/views/ops/deployments/index.vue");
const api = read("src/api/admin/sysdeploy.ts");
for (const token of ["PageTitleTabs", 'aria-label="服务部署"', "网关路由", "受保护", "显示已停用", "上报实例", "compiled_at", "canTogglePlacement", "routeSync"]) {
  if (!source.includes(token)) throw new Error(`deployment page missing ${token}`);
}
for (const method of ["GetCatalog", "ListHosts", "ListPlacements", "GetHostRoutes", "GetDirectory", "SetHostStatus", "SetPlacementStatus"]) {
  if (!api.includes(`"${method}"`)) throw new Error(`deployment API missing ${method}`);
}
for (const retired of ["ListServiceDeployments", "CreateServiceDeployment", "UpdateServiceDeployment", "DeleteServiceDeployment", "ListGatewayNodes", "CreateGatewayNode", "GetGatewayNodeRoutes"]) {
  if (api.includes(retired)) throw new Error(`retired API remains: ${retired}`);
}
for (const file of ["src/views/ops/service-management", "src/views/settings/service-deployments"]) {
  if (fs.existsSync(path.join(process.cwd(), file))) throw new Error(`retired page remains: ${file}`);
}
console.log("deployment frontend contract passed");
