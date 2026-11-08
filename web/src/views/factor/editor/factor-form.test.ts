import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";
import type { ValidateFactorSpecRsp } from "@/api/factor/types";
import type { DatasetColumn } from "@/api/storage/types";
import { CROSS_SECTION_SPEC_TEMPLATE, TIMESERIES_SPEC_TEMPLATE, isBuiltinTemplate, specTemplate } from "./editor-template";
import {
  copyFactorId,
  setPendingCopy,
  takePendingCopy,
  blankFactorForm,
  buildFactorPayload,
  checklistBlockers,
  compiledFor,
  diagnosticDocLine,
  diagnosticText,
  factorChecklist,
  filterFunctions,
  formFromDefinition,
  previewRequest,
  sourceInputColumns,
  specKey,
  type CompileState,
  type FactorFormState
} from "./factor-form";

const column = (name: string, patch: Partial<DatasetColumn> = {}): DatasetColumn => ({
  space_id: "crypto",
  dataset_id: "d",
  column_name: name,
  origin_type: "DATASET_COLUMN_ORIGIN_TYPE_FIELD",
  origin_id: name,
  value_type: "FIELD_VALUE_TYPE_DOUBLE",
  status: "COLUMN_STATUS_ACTIVE",
  ...patch
});

const valid = (patch: Partial<FactorFormState> = {}): FactorFormState => ({
  ...blankFactorForm(),
  factor_id: "bias",
  name: "Bias 偏离",
  spec: "outputs:\n  bias_20: close / ts_mean(close, 20) - 1\n",
  ...patch
});

const ok = (form: FactorFormState, factorType: "timeseries" | "cross_section" = "timeseries"): CompileState => ({
  status: "valid",
  spec: specKey(form),
  result: { ret_info: { code: 0, msg: "" }, valid: true, factor_type: factorType, outputs: ["bias_20"], lookback_periods: 20 }
});

const failing = (form: FactorFormState, compile: CompileState = ok(form)) =>
  factorChecklist(form, compile).filter(item => !item.ok);

describe("sourceInputColumns", () => {
  it("keeps active business columns and drops system or reserved ones", () => {
    expect(
      sourceInputColumns([
        column("close"),
        column("open"),
        column("data_time"),
        column("sys", { origin_type: "DATASET_COLUMN_ORIGIN_TYPE_SYSTEM" }),
        column("old", { status: "COLUMN_STATUS_RETIRED" })
      ])
    ).toEqual(["close", "open"]);
  });
});

describe("factorChecklist", () => {
  it("passes a compiled definition", () => {
    expect(failing(valid())).toEqual([]);
  });

  it("checks the factor id and display name", () => {
    expect(failing(valid({ factor_id: "" })).map(item => item.key)).toContain("factor_id");
    expect(failing(valid({ factor_id: "my-factor" })).map(item => item.key)).toContain("factor_id");
    expect(failing(valid({ name: "  " })).map(item => item.key)).toContain("name");
    expect(failing(valid({ name: "名".repeat(65) })).map(item => item.key)).toContain("name");
    expect(failing(valid({ name: "a\tb" })).map(item => item.key)).toContain("name");
    expect(failing(valid({ name: "中文展示名 Bias_20" }))).toEqual([]);
  });

  it("requires a compile result for the current text", () => {
    const form = valid();
    const stale: CompileState = {
      ...(ok(form) as Extract<CompileState, { status: "valid" }>),
      spec: specKey(valid({ spec: "x" }))
    };
    expect(failing(form, stale)[0].message).toContain("等待重新校验");
    expect(failing(form, { status: "checking" })[0].message).toContain("校验");
    expect(failing(valid({ spec: " \n" }), { status: "idle" })[0].message).toContain("不能为空");
    expect(failing(form, { status: "error", message: "网络错误" })[0].message).toContain("网络错误");
  });

  it("shows the server diagnostic with its position", () => {
    const form = valid({ spec: "params: {window: 20}\noutputs:\n  bias: close / nope(close, window)\n" });
    const result: ValidateFactorSpecRsp = {
      ret_info: { code: 0, msg: "" },
      valid: false,
      diagnostic: { path: "outputs.bias", doc_line: 3, line: 1, column: 9, message: "未知的函数 nope" }
    };
    const blockers = checklistBlockers(factorChecklist(form, { status: "invalid", spec: specKey(form), result }));
    expect(blockers).toEqual(["outputs.bias，定义第 3 行，第 9 列：未知的函数 nope"]);
    expect(diagnosticDocLine(result)).toBe(3);
    const multiline = { ...result, diagnostic: { ...result.diagnostic!, line: 2, column: 4 } };
    expect(diagnosticText(multiline)).toBe("outputs.bias，定义第 3 行，表达式第 2 行第 4 列：未知的函数 nope");
    expect(diagnosticText({ ret_info: { code: 0, msg: "" }, valid: false, diagnostic: { message: "定义为空" } })).toBe(
      "定义为空"
    );
  });

  it("allows partial universe only for cross-section results", () => {
    const form = valid({ allow_partial_universe: true });
    expect(failing(form, ok(form, "timeseries")).map(item => item.key)).toContain("allow_partial_universe");
    expect(failing(form, ok(form, "cross_section"))).toEqual([]);
  });

  it("treats allow_partial_universe as part of the compiled key", () => {
    const form = valid();
    const compile = ok(form);
    expect(compiledFor(compile, form)).toBe(true);
    expect(compiledFor(compile, { ...form, allow_partial_universe: true })).toBe(false);
  });
});

describe("payload", () => {
  it("sends only the definition text and identity", () => {
    expect(buildFactorPayload(valid({ factor_id: " Bias ", name: " Bias 偏离 " }), "timeseries")).toEqual({
      factor_id: "Bias",
      name: "Bias 偏离",
      spec: "outputs:\n  bias_20: close / ts_mean(close, 20) - 1\n",
      allow_partial_universe: false
    });
    expect(buildFactorPayload(valid({ allow_partial_universe: true }), "cross_section").allow_partial_universe).toBe(true);
    expect(buildFactorPayload(valid({ allow_partial_universe: true }), "timeseries").allow_partial_universe).toBe(false);
  });

  it("round-trips a stored definition", () => {
    const form = formFromDefinition({
      factor_id: "Rank",
      name: "动量排名",
      spec: CROSS_SECTION_SPEC_TEMPLATE,
      spec_hash: "sha256:x",
      factor_type: "cross_section",
      input_columns: ["close"],
      outputs: ["momentum_rank_20"],
      lookback_periods: 21,
      allow_partial_universe: true,
      created_at: "",
      updated_at: ""
    });
    expect(form).toEqual({
      factor_id: "Rank",
      name: "动量排名",
      spec: CROSS_SECTION_SPEC_TEMPLATE,
      allow_partial_universe: true,
      category_id: ""
    });
  });

  it("sends the category only when one is chosen", () => {
    const form = { ...blankFactorForm(), factor_id: "bias", name: "Bias" };
    expect(buildFactorPayload(form, "timeseries")).not.toHaveProperty("category_id");
    expect(buildFactorPayload({ ...form, category_id: "momentum" }, "timeseries").category_id).toBe("momentum");
  });
});

describe("templates and function reference", () => {
  it("offers time-series and cross-section templates", () => {
    expect(blankFactorForm().spec).toBe(TIMESERIES_SPEC_TEMPLATE);
    expect(specTemplate("cross_section")).toContain("cs_rank");
    expect(isBuiltinTemplate(TIMESERIES_SPEC_TEMPLATE)).toBe(true);
    expect(isBuiltinTemplate(`${TIMESERIES_SPEC_TEMPLATE}# edited`)).toBe(false);
  });

  it("filters functions by name, signature or doc", () => {
    const functions = [
      { name: "ts_mean", signature: "ts_mean(x, n)", doc: "最近 n 期的均值" },
      { name: "cs_rank", signature: "cs_rank(x)", doc: "截面排名" }
    ];
    expect(filterFunctions(functions, "")).toHaveLength(2);
    expect(filterFunctions(functions, "RANK").map(fn => fn.name)).toEqual(["cs_rank"]);
    expect(filterFunctions(functions, "均值").map(fn => fn.name)).toEqual(["ts_mean"]);
  });
});

describe("editor page contract", () => {
  const page = fs.readFileSync(path.resolve(__dirname, "index.vue"), "utf8");

  it("editor form has no 计算任务 / 源数据集 / 频率 field", () => {
    expect(page).not.toContain('label="计算任务"');
    expect(page).not.toContain('label="源数据集"');
    expect(page).not.toContain('label="频率"');
    expect(page).toContain("参考数据集");
  });

  it("editor never calls createFactorTask", () => {
    expect(page).not.toContain("createFactorTask");
    expect(page).toContain("createFactor");
    expect(page).toContain("updateFactor");
  });

  it("新建保存后换到编辑页的地址（留在编辑页），编辑保存后回列表；编辑页能添加到计算任务并预选这个因子", () => {
    expect(page).toContain("else await router.replace(`/factor/definitions/${encodeURIComponent(payload.factor_id)}/edit`)");
    expect(page).toContain('if (editing.value) goBackOr(router, "/factor/definitions")');
    expect(page).toContain(':preset-factor-id="factorId"');
  });

  it("editor protects unsaved changes on leave, reload and tab close", () => {
    expect(page).toContain("useUnsavedGuard(");
  });

  it("editor edits the YAML spec in the code editor and compiles it on the server", () => {
    expect(page).toContain("AsyncCodeEditor");
    expect(page).toContain('v-model="form.spec"');
    expect(page).toContain('language="yaml"');
    expect(page).toContain("validateFactorSpec");
    expect(page).toContain("previewFactor");
    expect(page).toContain("listFactorFunctions");
    expect(page).not.toMatch(/lookback_periods"|input_columns"|params_json/);
  });

  it("reference dataset is optional and not part of the payload", () => {
    expect(page).toContain("referenceDatasetId");
    expect(fs.readFileSync(path.resolve(__dirname, "factor-form.ts"), "utf8")).not.toContain("referenceDatasetId");
    expect(buildFactorPayload(valid())).not.toHaveProperty("referenceDatasetId");
  });
});

describe("因子 ID 与后端规则一致", () => {
  it.each(["Bias", "a__b", "_bias", "bias_", "1bias", "a".repeat(25)])("%j 在保存前检查里不通过", id => {
    const form = valid({ factor_id: id });
    expect(failing(form).map(item => item.key)).toContain("factor_id");
  });

  it.each(["bias", "bias_20", "ma5_ratio"])("%j 通过", id => {
    const form = valid({ factor_id: id });
    expect(failing(form).map(item => item.key)).not.toContain("factor_id");
  });

  it("试算请求带上因子 ID（去掉首尾空格），没填时不带", () => {
    const scope = { taskId: "t1", subjects: ["BTC-USDT"], periods: 20 };
    expect(previewRequest(valid({ factor_id: " bias_20 " }), scope)).toMatchObject({
      task_id: "t1",
      subjects: ["BTC-USDT"],
      periods: 20,
      factor_id: "bias_20"
    });
    expect(previewRequest(valid({ factor_id: "" }), scope).factor_id).toBeUndefined();
  });

  it("改了因子 ID 后旧的校验结果作废，单输出因子的输出名要按新 ID 重新推导", () => {
    const form = valid({ factor_id: "bias" });
    const compile = ok(form);
    expect(compiledFor(compile, form)).toBe(true);
    expect(compiledFor(compile, { ...form, factor_id: "bias_20" })).toBe(false);
  });
});

describe("另存为新因子", () => {
  it("ID 加 _v2，已有 _vN 的加一；超长时截断原 ID 且不留结尾下划线", () => {
    expect(copyFactorId("bias_20")).toBe("bias_20_v2");
    expect(copyFactorId("bias_20_v2")).toBe("bias_20_v3");
    const long = copyFactorId("abcdefghij_klmnopqrstu_x");
    expect(long.length).toBeLessThanOrEqual(24);
    expect(long).toBe("abcdefghij_klmnopqrst_v2");
    expect(copyFactorId("abcdefghijklmnopqrst_uvw")).toBe("abcdefghijklmnopqrst_v2");
  });

  it("带过去的定义只能取走一次", () => {
    setPendingCopy({ ...blankFactorForm(), factor_id: "a_v2" });
    expect(takePendingCopy()?.factor_id).toBe("a_v2");
    expect(takePendingCopy()).toBeNull();
  });
});
