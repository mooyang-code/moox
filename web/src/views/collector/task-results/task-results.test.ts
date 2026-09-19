import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

describe("collector result workflow", () => {
  it("renders every task result through the shared real-data browser", () => {
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    const browse = fs.readFileSync(path.resolve(__dirname, "../../data/view-browse/index.vue"), "utf8");
    expect(results).toContain("<ViewBrowse");
    expect(results).toContain(":active-view-id=");
    expect(results).toContain(':hide-technical-identity="true"');
    expect(results).toContain(':auto-refresh-interval-ms="30000"');
    expect(browse).toContain("<KlineModal");
    expect(browse).toContain('@click="openKlineModal"');
  });

  it("keeps result tabs keyed by task identity instead of Dataset IDs", () => {
    const results = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");
    expect(results).toContain('v-for="item in results"');
    expect(results).toContain(':key="item.task_id"');
    expect(results).toContain(":title=\"item.task_name || item.task_id\"");
    expect(results).not.toContain("dataset_id");
  });
});
