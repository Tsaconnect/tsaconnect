// react-test-renderer ships no types of its own and is not a direct dependency:
// it arrives pinned through jest-expo, which is why nothing outside a test may
// import it. Declared here as the narrow slice the tests use rather than pulling
// in @types/react-test-renderer for three call sites.
declare module 'react-test-renderer' {
  import type { ReactElement } from 'react';

  export interface ReactTestRenderer {
    toJSON(): unknown;
    unmount(): void;
  }

  export function create(element: ReactElement): ReactTestRenderer;
  export function act(callback: () => Promise<void> | void): Promise<void>;

  const TestRenderer: { create: typeof create; act: typeof act };
  export default TestRenderer;
}
