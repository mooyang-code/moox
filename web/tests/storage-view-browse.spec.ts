import { readFileSync } from "node:fs";
import { resolve } from "node:path";
import { describe, expect, it } from "vitest";
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
    expect(source).toContain("page: { page: pagination.current, size: DEFAULT_VIEW_PAGE_SIZE }");
    expect(source).toContain('total_mode: "NONE"');
    expect(source).toContain("VIEW_BROWSE_UNSCOPED_PREVIEW_WINDOW_HOURS = 24");
    expect(source).toContain("hasExactSubjectIDFilter()");
  });

  it("keeps the view status line focused on identity and actions", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/data/view-browse/index.vue"), "utf8");

    expect(source).not.toContain("频率: {{ currentViewFrequency }}");
    expect(source).not.toContain("{{ buildTimeText }}");
    expect(source).not.toContain("活跃版本 {{ activeView.active_view_revision }}");
  });

  it("uses dataset columns for filters and keeps the query pane within the window", () => {
    const source = readFileSync(resolve(__dirname, "../src/views/data/view-browse/index.vue"), "utf8");

    expect(source).toContain(
      "buildViewFilterFieldOptions(mode.value, viewColumns.value, datasetColumns.value, columnLabels.value)"
    );
    expect(source).toContain("await datasetColumnsPromise");
    expect(source).toContain("repeat(auto-fit, minmax(min(220px, 100%), 1fr))");
    expect(source).toContain(":scroll=\"{ x: 'max-content', y: 500 }\"");
    expect(source).toContain('fixed="right"');
    expect(source).toContain("overflow-x: hidden");
    expect(source).not.toContain("overflow-x: hidden !important");
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
