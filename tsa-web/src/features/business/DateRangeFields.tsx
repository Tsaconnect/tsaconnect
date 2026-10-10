import { Button } from '@/components/ui/button';
import { Input } from '@/components/ui/input';

export type DateRange = { from: string; to: string };

/**
 * A From/To pair of day inputs (YYYY-MM-DD). The caller owns the value; `problem`
 * is the reason the range can't be used (see periodProblem). Pass `clearLabel`
 * to offer a button that empties both days.
 */
export function DateRangeFields({ value, onChange, legend, caption, fromLabel = 'From', toLabel = 'To', problem, clearLabel }: {
  value: DateRange;
  onChange: (next: DateRange) => void;
  /** Read by screen readers only: "Created between". */
  legend: string;
  /** Shown before the inputs: "Created". */
  caption?: string;
  fromLabel?: string;
  toLabel?: string;
  problem?: string | null;
  clearLabel?: string;
}) {
  const { from, to } = value;
  return (
    <fieldset className="flex flex-wrap items-center gap-2">
      <legend className="sr-only">{legend}</legend>
      {caption && <span aria-hidden className="text-caption font-semibold text-text-secondary">{caption}</span>}
      <Input type="date" aria-label={fromLabel} value={from} max={to || undefined} className="h-9 w-[150px]"
        onChange={(e) => onChange({ from: e.target.value, to })} />
      <span aria-hidden className="text-text-secondary">–</span>
      <Input type="date" aria-label={toLabel} value={to} min={from || undefined} className="h-9 w-[150px]"
        onChange={(e) => onChange({ from, to: e.target.value })} />
      {clearLabel && (from || to) && (
        <Button variant="ghost" size="sm" onClick={() => onChange({ from: '', to: '' })}>{clearLabel}</Button>
      )}
      {problem && <p role="alert" className="basis-full text-caption text-danger-strong">{problem}</p>}
    </fieldset>
  );
}
