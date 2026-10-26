import { describe, expect, it } from "vitest";
import type { FactorMember, FactorTask, FactorTaskInfo } from "@/api/factor/types";
import { availableTasks, taskColumns } from "./task-options";

const task = (patch: Partial<FactorTask> = {}): FactorTask => ({
  task_id: "ftask_a",
  space_id: "crypto",
  source_dataset_id: "dataset_kline",
  freq: "1m",
  name: "现货因子",
  result_dataset_id: "dataset_factor_a",
  status: "enabled",
  ...patch
});

const member = (key: string, kind: FactorMember["kind"], columns: string[]): FactorMember => ({
  task_id: "ftask_a",
  factor_id: key.split("__")[0],
  member_key: key,
  kind,
  status: "enabled",
  factor: { factor_id: key } as FactorMember["factor"],
  output_columns: columns,
  created_at: "",
  updated_at: ""
});

const info = (patch: Partial<FactorTask> = {}, members: FactorMember[] = []): FactorTaskInfo => ({
  factor_task: task(patch),
  members
});

describe("availableTasks", () => {
  it("lists only running, unbound tasks of the space, sorted by name", () => {
    const options = availableTasks(
      [
        info({ task_id: "ftask_b", name: "B 任务" }),
        info({ task_id: "ftask_a", name: "A 任务" }),
        info({ task_id: "ftask_bound", name: "已关联", bound_instance_id: "i1" }),
        info({ task_id: "ftask_off", name: "已停用", status: "disabled" }),
        info({ task_id: "ftask_new", name: "创建中", status: "pending" }),
        info({ task_id: "ftask_other", name: "别的空间", space_id: "stock" })
      ],
      "crypto"
    );
    expect(options.map(item => item.taskId)).toEqual(["ftask_a", "ftask_b"]);
  });

  it("carries every column of the task and the variable members the strategy has to parameterise", () => {
    const [option] = availableTasks(
      [
        info({}, [
          member("ma", "fixed", ["ma"]),
          member("boll", "fixed", ["boll__upper", "boll__lower"]),
          member("bias__var", "var", ["bias__var"])
        ])
      ],
      "crypto"
    );
    expect(option.columns).toEqual(["ma", "boll__upper", "boll__lower", "bias__var"]);
    expect(option.varMembers).toEqual(["bias__var"]);
    expect(option).toMatchObject({ name: "现货因子", freq: "1m" });
  });

  it("falls back to the task id as the name and tolerates members without columns", () => {
    const [option] = availableTasks(
      [info({ name: "" }, [{ ...member("ma", "fixed", []), output_columns: undefined }])],
      "crypto"
    );
    expect(option.name).toBe("ftask_a");
    expect(taskColumns({ members: [] })).toEqual([]);
    expect(option.columns).toEqual([]);
  });
});
