import type { GetFunctionJobResponse, FunctionJobStatus } from "@/api/function-instance";

const terminalStatuses = new Set<FunctionJobStatus>([
  "FUNCTION_JOB_STATUS_SUCCESS",
  "FUNCTION_JOB_STATUS_FAILED",
  "FUNCTION_JOB_STATUS_PARTIAL"
]);

export interface FunctionJobPollerCallbacks {
  onUpdate?: (response: GetFunctionJobResponse) => void;
  onTerminal?: (response: GetFunctionJobResponse) => void;
  onError?: (error: unknown) => void;
  onPollingTimeout?: (jobId: string) => void;
}

export interface FunctionJobPoller {
  start(jobId: string, callbacks?: FunctionJobPollerCallbacks): Promise<void>;
  stop(): void;
  dispose(): void;
}

export function createFunctionJobPoller(
  query: (jobId: string) => Promise<GetFunctionJobResponse>,
  options: { intervalMs?: number; timeoutMs?: number } = {}
): FunctionJobPoller {
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

  const start = async (jobId: string, callbacks: FunctionJobPollerCallbacks = {}) => {
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
