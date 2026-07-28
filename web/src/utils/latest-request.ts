export interface LatestRequest {
  isLatest(): boolean;
}

export function createLatestRequestGuard() {
  let generation = 0;

  return {
    begin(): LatestRequest {
      const requestGeneration = ++generation;
      return {
        isLatest: () => requestGeneration === generation
      };
    },
    invalidate() {
      generation += 1;
    }
  };
}
