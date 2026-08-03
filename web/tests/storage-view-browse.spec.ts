import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
import { useViewCatalog } from "@/views/data/view-browse/composables/use-view-catalog";
import { useViewKline } from "@/views/data/view-browse/composables/use-view-kline";
import { useViewQuery } from "@/views/data/view-browse/composables/use-view-query";
import { buildViewFilterExprs } from "@/views/data/view-browse/view-browse-utils";

describe("storage view browse workflows", () => {
  it("uses previous and next controls instead of total-count pagination", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/data/view-browse/index.vue"), "utf8");

    expect(source).toContain(':pagination="false"');
    expect(source).not.toContain("showTotal");
    expect(source).not.toContain("showJumper");
    expect(source).not.toContain("@page-change");
    expect(source.match(/aria-label="上一页"/g)).toHaveLength(2);
    expect(source.match(/aria-label="下一页"/g)).toHaveLength(2);
  });

  it("queries time-series previews one page at a time and limits unscoped views to one day", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/data/view-browse/index.vue"), "utf8");

    expect(source).not.toContain("VIEW_BROWSE_PREVIEW_LIMIT");
    expect(source).toContain('page: { page: pagination.current, size: DEFAULT_VIEW_PAGE_SIZE }');
    expect(source).toContain('total_mode: "NONE"');
    expect(source).toContain("VIEW_BROWSE_UNSCOPED_PREVIEW_WINDOW_HOURS = 24");
    expect(source).toContain("hasExactSubjectIDFilter()");
  });

  it("selects the first remaining view when the active view disappears", () => {
    const catalog = useViewCatalog<{ view_id: string }>();
    catalog.replaceViews([{ view_id: "view-a" }, { view_id: "view-b" }]);
    catalog.activeViewId.value = "view-b";
    catalog.replaceViews([{ view_id: "view-a" }]);
    expect(catalog.activeViewId.value).toBe("view-a");
  });

  it("exposes query loading and error state", async () => {
    const query = useViewQuery<number>();
    await expect(query.run(async () => [1, 2])).resolves.toEqual([1, 2]);
    expect(query.loading.value).toBe(false);
    await expect(
      query.run(async () => {
        throw new Error("query failed");
      })
    ).rejects.toThrow("query failed");
    expect(query.error.value).toBe("query failed");
  });

  it("opens and closes the kline workflow", () => {
    const kline = useViewKline();
    kline.open();
    expect(kline.visible.value).toBe(true);
    kline.close();
    expect(kline.visible.value).toBe(false);
  });

  it("serializes empty filters with the current protobuf null enum", () => {
    expect(
      buildViewFilterExprs([
        { fieldName: "close", operator: "empty", valueType: "FIELD_VALUE_TYPE_DOUBLE" },
        { fieldName: "volume", operator: "not_empty", valueType: "FIELD_VALUE_TYPE_DOUBLE" }
      ])
    ).toEqual({
      group_logical: "FILTER_LOGICAL_AND",
      groups: [
        {
          logical: "FILTER_LOGICAL_AND",
          conds: [
            { column: "close", op: "FILTER_OP_EQ", values: [{ null_value: "NULL_VALUE_NULL" }] },
            { column: "volume", op: "FILTER_OP_NE", values: [{ null_value: "NULL_VALUE_NULL" }] }
          ]
        }
      ]
    });
  });

  it("serializes series tag emptiness against the canonical empty tag", () => {
    expect(
      buildViewFilterExprs([
        { fieldName: "series_tag", operator: "empty", valueType: "FIELD_VALUE_TYPE_STRING" },
        { fieldName: "series_tag", operator: "not_empty", valueType: "FIELD_VALUE_TYPE_STRING" }
      ])
    ).toEqual({
      group_logical: "FILTER_LOGICAL_AND",
      groups: [
        {
          logical: "FILTER_LOGICAL_AND",
          conds: [
            { column: "series_tag", op: "FILTER_OP_EQ", values: [{ string_value: "" }] },
            { column: "series_tag", op: "FILTER_OP_NE", values: [{ string_value: "" }] }
          ]
        }
      ]
    });
  });
});
