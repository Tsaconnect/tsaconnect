import { cn } from '@/lib/cn';

/** The look shared by every form control (Input, PasswordInput, Select). */
export const control = cn(
  'h-11 w-full rounded-md border-[1.5px] border-control-border bg-surface px-3 text-body-sm text-text-primary placeholder:text-text-muted',
  'transition-[border-color,box-shadow] duration-150 focus:border-accent focus:shadow-[0_0_0_3px_var(--focus-ring)] focus:outline-none focus-visible:outline-none',
  'disabled:cursor-not-allowed disabled:bg-[var(--surface-disabled)] disabled:text-[color:var(--text-disabled)]',
  'aria-[invalid=true]:border-danger aria-[invalid=true]:focus:shadow-[0_0_0_3px_var(--danger-bg)]',
);
