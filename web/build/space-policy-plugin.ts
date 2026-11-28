import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";
import type { Plugin } from "vite";
import { parse } from "yaml";

const VIRTUAL_ID = "virtual:space-policy";
const RESOLVED_VIRTUAL_ID = `\0${VIRTUAL_ID}`;
const CATALOG_PATH = fileURLToPath(new URL("../../packages/servicecatalog/catalog.yaml", import.meta.url));
const VALID_POLICIES = new Set(["global", "space_optional", "space_required"]);

export type SpacePolicy = "global" | "space_optional" | "space_required";

export interface SpacePolicyTable {
  servicePolicies: Record<string, SpacePolicy>;
  methodPolicies: Record<string, Record<string, SpacePolicy>>;
}

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

function isSpacePolicy(value: string): value is SpacePolicy {
  return VALID_POLICIES.has(value);
}

/** 按与后端 Catalog.ConsoleSpacePolicy 一致的规则解析服务级与方法级三档空间策略。 */
export function extractSpacePolicies(rawYAML: string, sourceLabel = CATALOG_PATH): SpacePolicyTable {
  const doc = parse(rawYAML) as RawCatalog | null;
  if (!doc || !Array.isArray(doc.components)) {
    throw new Error(`无法解析服务目录：${sourceLabel}`);
  }

  const servicePolicies: Record<string, SpacePolicy> = {};
  const methodPolicies: Record<string, Record<string, SpacePolicy>> = {};

  for (const component of doc.components) {
    for (const service of component.services ?? []) {
      const consoleName = service.console_name;
      if (service.space_policy !== undefined && !isSpacePolicy(service.space_policy)) {
        throw new Error(`服务 ${service.path ?? ""} 的 space_policy 非法：${service.space_policy}`);
      }
      if (!consoleName) {
        if (service.space_policy !== undefined) {
          throw new Error(`服务 ${service.path ?? ""} 未声明 console_name，不能设置 space_policy`);
        }
        continue;
      }
      if (service.space_policy === "global" || service.space_policy === "space_optional") {
        servicePolicies[consoleName] = service.space_policy;
      }
      for (const method of service.methods ?? []) {
        if (method.space_policy === undefined) continue;
        if (!isSpacePolicy(method.space_policy)) {
          throw new Error(`方法 ${service.path ?? ""}/${method.name ?? ""} 的 space_policy 非法：${method.space_policy}`);
        }
        if (!method.name) continue;
        (methodPolicies[consoleName] ??= {})[method.name] = method.space_policy;
      }
    }
  }

  return { servicePolicies, methodPolicies };
}

/** 解析单个控制台方法的最终策略：方法级覆盖服务级，未声明默认 space_required。 */
export function resolveSpacePolicy(table: SpacePolicyTable, service: string, method: string): SpacePolicy {
  return table.methodPolicies[service]?.[method] ?? table.servicePolicies[service] ?? "space_required";
}

function formatPolicyRecord(entries: Record<string, SpacePolicy>): string {
  const keys = Object.keys(entries).sort();
  if (keys.length === 0) return "{}";
  const lines = keys.map(key => `  ${JSON.stringify(key)}: ${JSON.stringify(entries[key])}`);
  return `{\n${lines.join(",\n")}\n}`;
}

function formatMethodPolicyMap(entries: Record<string, Record<string, SpacePolicy>>): string {
  const services = Object.keys(entries).sort();
  if (services.length === 0) return "{}";
  const lines = services.map(service => {
    const methods = entries[service];
    const methodKeys = Object.keys(methods).sort();
    const inner = methodKeys.map(method => `    ${JSON.stringify(method)}: ${JSON.stringify(methods[method])}`).join(",\n");
    return `  ${JSON.stringify(service)}: {\n${inner}\n  }`;
  });
  return `{\n${lines.join(",\n")}\n}`;
}

export function generateSpacePolicyModuleFromYAML(rawYAML: string, sourceLabel = CATALOG_PATH): string {
  const { servicePolicies, methodPolicies } = extractSpacePolicies(rawYAML, sourceLabel);
  return [
    "// 构建时由 packages/servicecatalog/catalog.yaml 自动生成，禁止手工修改。",
    `export const SERVICE_SPACE_POLICIES = ${formatPolicyRecord(servicePolicies)};`,
    `export const METHOD_SPACE_POLICIES = ${formatMethodPolicyMap(methodPolicies)};`,
    ""
  ].join("\n");
}

function generateSpacePolicyModule(): string {
  return generateSpacePolicyModuleFromYAML(readFileSync(CATALOG_PATH, "utf8"), CATALOG_PATH);
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
