// hooks/useSubmitSlot.ts
//
// One submit at a time. `submitting` (state) drives the spinner, but it still
// reads false to a second tap that lands before the re-render, so the slot is
// held by a ref from the first tap: begin() returns false while it is taken.
import { useCallback, useRef, useState } from 'react';

export interface SubmitSlot {
  submitting: boolean;
  /** Take the slot; false when a submit is already running. */
  begin: () => boolean;
  /** Release the slot (call in `finally`). */
  end: () => void;
}

export function useSubmitSlot(): SubmitSlot {
  const [submitting, setSubmitting] = useState(false);
  const held = useRef(false);
  const begin = useCallback(() => {
    if (held.current) return false;
    held.current = true;
    setSubmitting(true);
    return true;
  }, []);
  const end = useCallback(() => {
    held.current = false;
    setSubmitting(false);
  }, []);
  return { submitting, begin, end };
}
