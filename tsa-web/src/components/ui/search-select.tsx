import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from 'react';
import { Check, ChevronDown } from 'lucide-react';
import { cn } from '@/lib/cn';
import { control } from './control';

export type SearchOption = { value: string; label: string };

type Props = {
  options: SearchOption[];
  value: string;
  onChange: (value: string) => void;
  placeholder?: string;
  emptyText?: string;
  // Field passes these to its control.
  id?: string;
  'aria-describedby'?: string;
  'aria-invalid'?: boolean;
};

/** Options matching the query, those that start with it first. */
function matches(options: SearchOption[], query: string): SearchOption[] {
  const q = query.trim().toLowerCase();
  if (!q) return options;
  const starts: SearchOption[] = [];
  const contains: SearchOption[] = [];
  for (const o of options) {
    const label = o.label.toLowerCase();
    if (label.startsWith(q)) starts.push(o);
    else if (label.includes(q)) contains.push(o);
  }
  return [...starts, ...contains];
}

/**
 * A select you can type into to narrow a long list (banks). An ARIA combobox:
 * the input is the search, arrows move through the matches, Enter picks one.
 */
export function SearchSelect({ options, value, onChange, placeholder = 'Choose…', emptyText = 'No matches', ...aria }: Props) {
  const listId = useId();
  const listRef = useRef<HTMLUListElement>(null);
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState('');
  const [active, setActive] = useState(0);
  const selected = options.find((o) => o.value === value);
  const shown = useMemo(() => matches(options, query), [options, query]);

  useEffect(() => {
    if (open) listRef.current?.children[active]?.scrollIntoView?.({ block: 'nearest' });
  }, [open, active]);

  function show() {
    setQuery('');
    // Nothing highlighted until the user moves or types: Enter on a fresh field submits the form.
    setActive(options.findIndex((o) => o.value === value));
    setOpen(true);
  }

  function pick(o: SearchOption) {
    onChange(o.value);
    setOpen(false);
  }

  function onKeyDown(e: KeyboardEvent<HTMLInputElement>) {
    if (e.key === 'ArrowDown' || e.key === 'ArrowUp') {
      e.preventDefault();
      if (!open) return show();
      const step = e.key === 'ArrowDown' ? 1 : -1;
      setActive((i) => (shown.length ? Math.min(Math.max(i + step, 0), shown.length - 1) : -1));
    } else if (e.key === 'Enter' && open) {
      setOpen(false);
      if (shown[active]) {
        // Picks instead of submitting the form.
        e.preventDefault();
        pick(shown[active]);
      }
    } else if (e.key === 'Escape' && open) {
      // Closes the list only, not a dialog it sits in.
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
    }
  }

  const activeId = open && shown[active] ? `${listId}-${active}` : undefined;
  const count = shown.length === 0 ? emptyText : `${shown.length} ${shown.length === 1 ? 'result' : 'results'}`;

  return (
    <div className="relative">
      <input
        {...aria}
        role="combobox"
        aria-expanded={open}
        aria-controls={listId}
        aria-autocomplete="list"
        aria-activedescendant={activeId}
        autoComplete="off"
        spellCheck={false}
        className={cn(control, 'pr-10', !open && !selected && 'placeholder:text-text-primary')}
        value={open ? query : (selected?.label ?? '')}
        placeholder={open ? 'Search' : placeholder}
        onFocus={show}
        onClick={() => !open && show()}
        onBlur={() => setOpen(false)}
        onChange={(e) => {
          setQuery(e.target.value);
          // The best match is ready for Enter; clearing the search clears the highlight.
          setActive(e.target.value.trim() ? 0 : -1);
          setOpen(true);
        }}
        onKeyDown={onKeyDown}
      />
      <ChevronDown aria-hidden className="pointer-events-none absolute right-3 top-1/2 size-4 -translate-y-1/2 text-control-icon" />
      <div
        hidden={!open}
        className="absolute inset-x-0 top-full z-20 mt-1 rounded-md border border-border bg-surface py-1 shadow-[var(--shadow-lg)]"
      >
        <ul ref={listRef} id={listId} role="listbox" className="max-h-64 overflow-y-auto">
          {open &&
            shown.map((o, i) => (
              <li
                key={o.value}
                id={`${listId}-${i}`}
                role="option"
                aria-selected={o.value === value}
                // Keeps the focus in the input so blur doesn't close the list before the pick.
                onMouseDown={(e) => e.preventDefault()}
                onClick={() => pick(o)}
                className={cn(
                  'flex cursor-pointer items-center justify-between gap-2 px-3 py-2 text-body-sm text-text-primary hover:bg-surface-alt',
                  i === active && 'bg-surface-alt',
                )}
              >
                <span className="truncate">{o.label}</span>
                {o.value === value && <Check aria-hidden className="size-4 shrink-0 text-accent-strong" />}
              </li>
            ))}
        </ul>
        {open && shown.length === 0 && <p aria-hidden className="px-3 py-2 text-body-sm text-text-secondary">{emptyText}</p>}
      </div>
      <p role="status" className="sr-only">{open ? count : ''}</p>
    </div>
  );
}
