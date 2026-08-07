export interface ViewStateInput {
  loading: boolean;
  hasData: boolean;
  error: boolean;
  online: boolean;
  empty: boolean;
  usingTestData: boolean;
}

export function deriveViewState(input: ViewStateInput) {
  return {
    loading: input.loading,
    stale: input.error && input.hasData,
    error: input.error,
    offline: !input.online,
    empty: input.empty,
    testData: input.usingTestData,
  };
}
