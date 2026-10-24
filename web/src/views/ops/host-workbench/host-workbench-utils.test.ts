import { describe, expect, it } from "vitest";
import type { DeploymentHost } from "@/api/admin/types";
import { sshAvailability } from "./host-workbench-utils";

const host = (overrides: Partial<DeploymentHost> = {}): DeploymentHost => ({
  host_id: "control",
  label: "控制机",
  address: "203.0.113.10",
  status: "enabled",
  ssh_port: 22,
  ssh_user: "ubuntu",
  ...overrides
});

describe("sshAvailability", () => {
  it("allows hosts with a registered SSH port", () => {
    expect(sshAvailability(host())).toEqual({ ok: true, reason: "" });
  });

  it("greys out hosts without SSH and explains how to register one", () => {
    for (const item of [host({ ssh_port: 0 }), host({ ssh_port: undefined })]) {
      const result = sshAvailability(item);
      expect(result.ok).toBe(false);
      expect(result.reason).toContain("moox skill");
    }
  });
});
