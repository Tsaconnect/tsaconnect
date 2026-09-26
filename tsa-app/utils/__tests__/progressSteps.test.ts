import { sequenceSteps } from '../progressSteps';

const states = (steps: { key: string; state: string }[]) => steps.map((s) => `${s.key}:${s.state}`);

describe('sequenceSteps', () => {
  const defs = [{ key: 'a', label: 'A', detail: 'when A' }, { key: 'b', label: 'B', detail: 'when B' }, { key: 'c', label: 'C' }];

  it('marks before, at and after the reached step', () => {
    expect(states(sequenceSteps(defs, 1))).toEqual(['a:done', 'b:active', 'c:pending']);
    expect(states(sequenceSteps(defs, 3))).toEqual(['a:done', 'b:done', 'c:done']);
  });

  it('gives the active step its waiting line, and a pending step no time', () => {
    const steps = sequenceSteps(defs, 0, 'waiting');
    expect(steps[0].detail).toBe('waiting');
    expect(steps[1].detail).toBeUndefined();
  });
});
