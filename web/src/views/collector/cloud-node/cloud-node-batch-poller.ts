import type { GetNodeBatchChangeResponse, NodeBatchStatus } from "@/api/cloud-node";

const terminalStatuses = new Set<NodeBatchStatus>([
  "NODE_BATCH_STATUS_SUCCESS",
  "NODE_BATCH_STATUS_FAILED",
  "NODE_BATCH_STATUS_PARTIAL"
]);

export interface CloudNodeBatchPollerCallbacks {
  onUpdate?: (response: GetNodeBatchChangeResponse) => void;
  onTerminal?: (response: GetNodeBatchChangeResponse) => void;
  onError?: (error: unknown) => void;
  onPollingTimeout?: (jobId: string) => void;
}

export interface CloudNodeBatchPoller {
  start(jobId: string, callbacks?: CloudNodeBatchPollerCallbacks): Promise<void>;
  stop(): void;
  dispose(): void;
}

export function createCloudNodeBatchPoller(
  query: (jobId: string) => Promise<GetNodeBatchChangeResponse>,
  options: { intervalMs?: number; timeoutMs?: number } = {}
): CloudNodeBatchPoller {
  const intervalMs = options.intervalMs ?? 2000;
  const timeoutMs = options.timeoutMs ?? 30 * 60 * 1000;
  let generation = 0;
  let timer: ReturnType<typeof setTimeout> | undefined;
  let timeout: ReturnType<typeof setTimeout> | undefined;

  const stop = () => {
    generation++;
    if (timer !== undefined) clearTimeout(timer);
    if (timeout !== undefined) clearTimeout(timeout);
    timer = undefined;
    timeout = undefined;
  };

  const start = async (jobId: string, callbacks: CloudNodeBatchPollerCallbacks = {}) => {
    stop();
    const activeGeneration = generation;
    let timedOut = false;

    timeout = setTimeout(() => {
      if (activeGeneration !== generation) return;
      timedOut = true;
      if (timer !== undefined) clearTimeout(timer);
      timer = undefined;
      callbacks.onPollingTimeout?.(jobId);
    }, timeoutMs);

    const poll = async (): Promise<void> => {
      if (activeGeneration !== generation || timedOut) return;
      try {
        const response = await query(jobId);
        if (activeGeneration !== generation || timedOut) return;
        callbacks.onUpdate?.(response);
        if (terminalStatuses.has(response.job.status)) {
          if (timeout !== undefined) clearTimeout(timeout);
          timeout = undefined;
          callbacks.onTerminal?.(response);
          return;
        }
      } catch (error) {
        if (activeGeneration !== generation || timedOut) return;
        callbacks.onError?.(error);
      }
      timer = setTimeout(() => void poll(), intervalMs);
    };

    await poll();
  };

  return { start, stop, dispose: stop };
}
