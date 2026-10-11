// The ad screen's open guard: a fast double tap must not start a second open.
import React from 'react';
import TestRenderer, { act } from 'react-test-renderer';
import { useSubmitSlot, type SubmitSlot } from '../useSubmitSlot';

let slot: SubmitSlot;
function Screen() {
  slot = useSubmitSlot();
  return null;
}

it('a second tap before the re-render is refused; the slot frees on end()', () => {
  act(() => { TestRenderer.create(<Screen />); });
  const first = slot;
  let a = false;
  let b = true;
  // Both taps run against the same render (same closure), as a double tap does.
  act(() => {
    a = first.begin();
    b = first.begin();
  });
  expect([a, b]).toEqual([true, false]);
  expect(slot.submitting).toBe(true);
  act(() => slot.end());
  expect(slot.submitting).toBe(false);
  let again = false;
  act(() => { again = slot.begin(); });
  expect(again).toBe(true);
});
