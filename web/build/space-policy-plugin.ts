import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";
import { parse } from "yaml";

const VIRTUAL_ID = "virtual:space-policy";
const RESOLVED_VIRTUAL_ID = `\0${VIRTUAL_ID}`;
const CATALOG_PATH = fileURLToPath(new URL("../../packages/servicecatalog/catalog.yaml", import.meta.url));
const VALID_POLICIES = new Set(["global", "space_optional", "space_required"]);

interface RawCatalogMethod {
  name?: string;
  space_policy?: string;
}

interface RawCatalogService {
  path?: string;
  console_name?: string;
  space_policy?: string;
  methods?: RawCatalogMethod[];
}

interface RawCatalogComponent {
  services?: RawCatalogService[];
}

interface RawCatalog {
  components?: RawCatalogComponent[];
}

function generateSpacePolicyModule(): string {
  const raw = readFileSync(CATALOG_PATH, "utf8");
  const doc = parse(raw) as RawCatalog | null;
  if (!doc || !Array.isArray(doc.components)) {
    throw new Error(`无法解析服务目录：${CATALOG_PATH}`);
  }

  const globalServices = new Set<string>();
  const globalMethods: Record<string, Set<string>> = {};
  const spaceOptionalMethods: Record<string, Set<string>> = {};

  for (const component of doc.components) {
    for (const service of component.services ?? []) {
      const consoleName = service.console_name;
      if (service.space_policy !== undefined && !VALID_POLICIES.has(service.space_policy)) {
        throw new Error(`服务 ${service.path ?? ""} 的 space_policy 非法：${service.space_policy}`);
      }
      if (!consoleName) {
        if (service.space_policy !== undefined) {
          throw new Error(`服务 ${service.path ?? ""} 未声明 console_name，不能设置 space_policy`);
        }
        continue;
      }
      if (service.space_policy === "global") {
        globalServices.add(consoleName);
      }
      for (const method of service.methods ?? []) {
        if (method.space_policy === undefined) continue;
        if (!VALID_POLICIES.has(method.space_policy)) {
          throw new Error(`方法 ${service.path ?? ""}/${method.name ?? ""} 的 space_policy 非法：${method.space_policy}`);
        }
        if (!method.name) continue;
        if (method.space_policy === "global") {
          (globalMethods[consoleName] ??= new Set()).add(method.name);
        } else if (method.space_policy === "space_optional") {
          (spaceOptionalMethods[consoleName] ??= new Set()).add(method.name);
        }
      }
    }
  }

  const formatSetMap = (entries: Record<string, Set<string>>): string => {
    const lines = Object.keys(entries)
      .sort()
      .map(key => `  ${JSON.stringify(key)}: new Set(${JSON.stringify([...entries[key]].sort())})`);
    return `{\n${lines.join(",\n")}\n}`;
  };

  return [
    "// 构建时由 packages/servicecatalog/catalog.yaml 自动生成，禁止手工修改。",
    `export const GLOBAL_SERVICES = new Set(${JSON.stringify([...globalServices].sort())});`,
    `export const GLOBAL_METHODS = ${formatSetMap(globalMethods)};`,
    `export const SPACE_OPTIONAL_METHODS = ${formatSetMap(spaceOptionalMethods)};`,
    ""
  ].join("\n");
}

/** 构建时从 packages/servicecatalog/catalog.yaml 生成控制台方法的空间策略表。 */
export function createSpacePolicyPlugin(): Plugin {
  return {
    name: "moox-space-policy",
    resolveId(id) {
      if (id === VIRTUAL_ID) return RESOLVED_VIRTUAL_ID;
      return null;
    },
    load(id) {
      if (id !== RESOLVED_VIRTUAL_ID) return null;
      if (typeof this.addWatchFile === "function") {
        this.addWatchFile(CATALOG_PATH);
      }
      return generateSpacePolicyModule();
    },
    configureServer(server) {
      server.watcher.add(CATALOG_PATH);
      server.watcher.on("change", file => {
        if (resolve(file) !== CATALOG_PATH) return;
        const mod = server.moduleGraph.getModuleById(RESOLVED_VIRTUAL_ID);
        if (mod) {
          server.moduleGraph.invalidateModule(mod);
          server.ws.send({ type: "full-reload" });
        }
      });
    }
  };
}
