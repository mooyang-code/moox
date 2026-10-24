import { describe, expect, it } from "vitest";
import type { HostMetrics } from "@/api/modules/host-monitor";
import type { DeploymentHost } from "@/api/admin/types";
import { buildHostMonitorRows, countUnregisteredAgents, normalizeMonitorViewMode } from "./host-monitor-mapping";

function monitor(overrides: Partial<HostMetrics> = {}): HostMetrics {
  return {
    agent_id: "agent-1",
    host_id: "control",
    hostname: "VM-0-1-ubuntu",
    status: "online",
    timestamp: "2026-07-15T02:00:00Z",
    cpu: { usage: 24, usage_available: true, cores: 4 },
    memory: { total: 100, used: 38, available: 62, percent: 38, percent_available: true },
    filesystems: [
      {
        device: "/dev/vda1",
        mountpoint: "/",
        fs_type: "ext4",
        total: 100,
        used: 67,
        available: 33,
        percent: 67,
        percent_available: true,
        read_only: false
      }
    ],
    disks: [],
    networks: [
      {
        device: "eth0",
        operstate: "up",
        rx_speed: 100,
        tx_speed: 40,
        receive_errors_total: 0,
        transmit_errors_total: 0,
        rate_available: true
      }
    ],
    storage_available: true,
    ...overrides
  };
}

function host(overrides: Partial<DeploymentHost> = {}): DeploymentHost {
  return { host_id: "control", label: "控制机", address: "203.0.113.10", status: "enabled", ssh_port: 22, ...overrides };
}

describe("buildHostMonitorRows", () => {
  it("lists registered hosts with the Chinese label, host id and address", () => {
    const rows = buildHostMonitorRows([host()], [monitor()]);
    expect(rows).toHaveLength(1);
    expect(rows[0]).toMatchObject({
      key: "control",
      hostId: "control",
      displayName: "控制机",
      displayAddress: "203.0.113.10",
      state: "online"
    });
    expect(rows[0].monitor?.agent_id).toBe("agent-1");
  });

  it("falls back to the host id when no label is registered and shows unreported hosts", () => {
    const rows = buildHostMonitorRows([host({ host_id: "storage", label: "" })], []);
    expect(rows[0]).toMatchObject({ displayName: "storage", state: "unmonitored", monitor: undefined });
  });

  it("uses the most recently reported agent when a host has several", () => {
    const rows = buildHostMonitorRows(
      [host()],
      [
        monitor({ agent_id: "old", timestamp: "2026-07-15T01:00:00Z" }),
        monitor({ agent_id: "new", timestamp: "2026-07-15T02:00:00Z" }),
        monitor({ agent_id: "older", timestamp: "2026-07-14T01:00:00Z" })
      ]
    );
    expect(rows).toHaveLength(1);
    expect(rows[0].monitor?.agent_id).toBe("new");
  });

  it("never turns an unregistered agent into a row and only counts it", () => {
    const monitors = [
      monitor({ agent_id: "stray", host_id: "" }),
      monitor({ agent_id: "ghost", host_id: "retired-host" }),
      monitor({ agent_id: "stray2", host_id: "" })
    ];
    const hosts = [host()];
    expect(buildHostMonitorRows(hosts, monitors).map(row => row.key)).toEqual(["control"]);
    expect(buildHostMonitorRows(hosts, monitors)[0].state).toBe("unmonitored");
    expect(countUnregisteredAgents(hosts, monitors)).toBe(3);
  });

  it("derives summaries, attention, and aggregate network rates", () => {
    const rows = buildHostMonitorRows(
      [host()],
      [
        monitor({
          cpu: { usage: 81.2, usage_available: true, cores: 4 },
          filesystems: [
            {
              device: "/dev/a",
              mountpoint: "/",
              fs_type: "ext4",
              total: 100,
              used: 30,
              available: 70,
              percent: 30,
              percent_available: true,
              read_only: false
            },
            {
              device: "/dev/b",
              mountpoint: "/data",
              fs_type: "ext4",
              total: 100,
              used: 78,
              available: 22,
              percent: 78,
              percent_available: true,
              read_only: false
            }
          ],
          networks: [
            {
              device: "eth0",
              operstate: "up",
              rx_speed: 100,
              tx_speed: 40,
              receive_errors_total: 0,
              transmit_errors_total: 0,
              rate_available: true
            },
            {
              device: "eth1",
              operstate: "up",
              rx_speed: 20,
              tx_speed: 10,
              receive_errors_total: 0,
              transmit_errors_total: 0,
              rate_available: true
            }
          ]
        })
      ]
    );

    expect(rows[0]).toMatchObject({
      cpuUsage: 81,
      memoryUsage: 38,
      filesystemUsage: 78,
      networkRate: { rx: 120, tx: 50 },
      attention: true
    });
  });

  it("does not render offline or unavailable values as zero", () => {
    const rows = buildHostMonitorRows(
      [host()],
      [
        monitor({
          status: "offline",
          cpu: { usage: 0, usage_available: true, cores: 4 },
          memory: { total: 100, used: 0, available: 100, percent: 0, percent_available: true }
        })
      ]
    );

    expect(rows[0]).toMatchObject({
      state: "offline",
      cpuUsage: null,
      memoryUsage: null,
      filesystemUsage: null,
      networkRate: null,
      attention: false
    });
  });
});

describe("normalizeMonitorViewMode", () => {
  it("defaults to cards and accepts master", () => {
    expect(normalizeMonitorViewMode(null)).toBe("cards");
    expect(normalizeMonitorViewMode("unexpected")).toBe("cards");
    expect(normalizeMonitorViewMode("master")).toBe("master");
  });
});
