import type { ReactNode } from 'react';
import { Link, useLocation } from 'react-router-dom';
import { Menu } from 'lucide-react';
import { cn } from '@/lib/cn';
import { withMode, type Mode } from './mode';

function ModeSwitch({ mode }: { mode: Mode }) {
  const { pathname, search } = useLocation();
  const option = (m: Mode, label: string) => (
    <Link
      to={withMode(pathname, search, m)}
      aria-current={mode === m ? 'page' : undefined}
      className={cn(
        'rounded-full px-3 py-[5px] text-[12.5px] font-semibold text-text-secondary transition-colors hover:text-text-primary',
        mode === m && (m === 'test' ? 'bg-gold-100 text-accent-strong hover:text-accent-strong' : 'bg-surface text-text-primary shadow-[var(--shadow-sm)]'),
      )}
    >
      {label}
    </Link>
  );
  return (
    <div role="group" aria-label="Mode" className="inline-flex shrink-0 rounded-full border border-hairline bg-background-alt p-[3px]">
      {option('live', 'Live')}
      {option('test', 'Test')}
    </div>
  );
}

export function TopBar({ title, actions, mode, onMenu }: { title: string; actions?: ReactNode; mode: Mode | null; onMenu: () => void }) {
  return (
    <header className="flex min-h-[61px] items-center gap-3 border-b border-hairline bg-surface px-4 py-3 sm:px-7">
      <button
        type="button"
        onClick={onMenu}
        aria-label="Open menu"
        className="-ml-1.5 grid size-9 shrink-0 place-items-center rounded-sm text-text-secondary hover:bg-surface-alt lg:hidden"
      >
        <Menu className="size-5" aria-hidden />
      </button>
      <h1 className="min-w-0 flex-1 truncate text-title2 font-semibold">{title}</h1>
      {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      {mode && <ModeSwitch mode={mode} />}
    </header>
  );
}
