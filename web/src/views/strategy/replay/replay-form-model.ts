/** 回放表单的状态：来源三选一（实例会话 / 已保存定义 / DSL 草稿），其余为 View、区间与手续费。 */
export interface ReplayFormState {
  source: "definition" | "dsl";
  strategy_id: string;
  dsl_yaml: string;
  instance_id: string;
  view_id: string;
  start: string;
  end: string;
  fee_bps: number;
}
