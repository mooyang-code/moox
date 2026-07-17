import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

const normalizeSource = (source: string) => source.replace(/\s+/g, "").replace(/'/g, '"');

describe("storage configuration workbench", () => {
  it("collects the storage pages into one ordered tab surface", () => {
    const source = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const normalized = normalizeSource(source);
    const positions = ["主存节点", "主存路由", "归档文件"].map(label => normalized.indexOf(`label:"${label}"`));

    expect(source).toContain("PageTitleTabs");
    expect(source).toContain('aria-label="存储配置"');
    expect(positions.every(position => position >= 0)).toBe(true);
    expect(positions).toEqual([...positions].sort((left, right) => left - right));
    expect(normalized).toContain('typeStorageConfigTab="nodes"|"routes"|"archive"');
  });

  it("keeps one visible storage menu and redirects legacy child URLs to tabs", () => {
    const menu = fs.readFileSync(path.resolve(__dirname, "../../../api/modules/system/static-menu.ts"), "utf8");
    const routes = fs.readFileSync(path.resolve(__dirname, "../../../router/route.ts"), "utf8");
    const normalizedMenu = normalizeSource(menu);
    const normalizedRoutes = normalizeSource(routes);

    expect(normalizedMenu).toContain('menu("0606","06","/ops/storage/nodes","ops-storage"');
    expect(normalizedMenu).not.toContain('menu("060601"');
    expect(normalizedMenu).not.toContain('menu("060602"');
    expect(normalizedMenu).not.toContain('menu("060603"');
    expect(normalizedRoutes).toContain('component:()=>import("@/views/ops/storage/index.vue")');
    expect(normalizedRoutes).toContain('redirect:{path:"/ops/storage/nodes",query:{tab:"routes"}}');
    expect(normalizedRoutes).toContain('redirect:{path:"/ops/storage/nodes",query:{tab:"archive"}}');
  });
});
