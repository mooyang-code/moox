import { describe, expect, it } from "vitest";
import { buildViewFilterExprs } from "@/views/data/view-browse/view-browse-utils";

describe("storage view browse workflows", () => {
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
