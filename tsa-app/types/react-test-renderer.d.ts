// react-test-renderer ships no types of its own and is not a direct dependency:
// it arrives pinned through jest-expo, which is why nothing outside a test may
// import it. Declared here as the narrow slice the tests use rather than pulling
// in @types/react-test-renderer for three call sites.
declare module 'react-test-renderer' {
  import type { ReactElement } from 'react';

  /** A node of the rendered tree — the slice tests query and drive. */
  export interface ReactTestInstance {
    // Deliberately loose: a test reaches for whatever prop the component under
    // test declares, and narrowing that here would mean restating each one.
    props: Record<string, any>;
    findAll(predicate: (node: ReactTestInstance) => boolean): ReactTestInstance[];
  }

  export interface ReactTestRenderer {
    toJSON(): unknown;
    unmount(): void;
    /** Re-render the same tree, as a parent re-rendering would. */
    update(element: ReactElement): void;
    root: ReactTestInstance;
  }

  export function create(element: ReactElement): ReactTestRenderer;
  export function act(callback: () => Promise<void> | void): Promise<void>;

  const TestRenderer: { create: typeof create; act: typeof act };
  export default TestRenderer;
}
