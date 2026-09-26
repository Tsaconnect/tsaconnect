import type { ReactNode } from 'react';
import { Link } from 'react-router-dom';
import { cn } from '@/lib/cn';

// chips: a wrapping row of pills (a filter). tabs: equal segments on a light track.
// slate: the same segments on the dark sidebar.
const looks = {
  chips: {
    group: 'flex flex-wrap gap-2',
    item: 'rounded-full border px-3 py-[5px] text-[12.5px] font-medium',
    on: 'border-text-primary bg-text-primary text-white',
    off: 'border-hairline bg-surface text-text-secondary hover:border-border hover:text-text-primary',
  },
  tabs: {
    group: 'grid auto-cols-fr grid-flow-col rounded-md border border-hairline bg-background-alt p-[3px]',
    item: 'rounded-[9px] px-3.5 py-1.5 text-center text-[12.5px] leading-[17px] font-semibold',
    on: 'bg-surface text-text-primary shadow-[var(--shadow-sm)]',
    off: 'text-text-secondary hover:text-text-primary',
  },
  slate: {
    group: 'grid auto-cols-fr grid-flow-col rounded-md border border-slate-raised bg-slate-elevated p-[3px]',
    item: 'rounded-[9px] px-3.5 py-1.5 text-center text-[12.5px] leading-[17px] font-semibold',
    on: 'bg-slate-raised text-white',
    off: 'text-text-muted hover:text-white',
  },
};

type Look = keyof typeof looks;

const segment = (look: Look, on: boolean) => cn('transition-colors', looks[look].item, on ? looks[look].on : looks[look].off);

/** One choice from a few, as native radios styled as chips or tabs (arrow keys move between them). */
export function Segmented<T extends string>({ legend, name, value, options, onChange, look = 'tabs', className }: {
  legend: string;
  name: string;
  value: T;
  options: readonly { value: T; label: ReactNode }[];
  onChange: (value: T) => void;
  look?: Exclude<Look, 'slate'>;
  className?: string;
}) {
  return (
    <fieldset className={cn(looks[look].group, className)}>
      <legend className="sr-only">{legend}</legend>
      {options.map((o) => (
        <label
          key={o.value || '-'}
          className={cn('cursor-pointer has-[:focus-visible]:shadow-[0_0_0_3px_var(--focus-ring)]', segment(look, o.value === value))}
        >
          <input type="radio" name={name} value={o.value} checked={o.value === value} onChange={() => onChange(o.value)} className="sr-only" />
          {o.label}
        </label>
      ))}
    </fieldset>
  );
}

/** The same segments as navigation: links, the current one marked aria-current. */
export function SegmentedNav({ label, items, look = 'tabs', className }: {
  label: string;
  items: readonly { to: string; label: ReactNode; current: boolean; onClick?: () => void }[];
  look?: Exclude<Look, 'chips'>;
  className?: string;
}) {
  return (
    <nav aria-label={label} className={cn(looks[look].group, className)}>
      {items.map((i) => (
        <Link key={i.to} to={i.to} onClick={i.onClick} aria-current={i.current ? 'page' : undefined} className={segment(look, i.current)}>
          {i.label}
        </Link>
      ))}
    </nav>
  );
}
