import { cn } from '@/lib/cn';

/** The gold "T" tile. */
export function BrandMark({ className }: { className?: string }) {
  return (
    <span
      aria-hidden
      className={cn('grid size-7 shrink-0 place-items-center rounded-sm bg-[linear-gradient(135deg,var(--gold-bright),var(--accent-hover))] text-[13px] font-extrabold text-slate-950', className)}
    >
      T
    </span>
  );
}

/** Mark + wordmark, for dark surfaces. */
export function Brand({ className }: { className?: string }) {
  return (
    <span className={cn('flex items-center gap-2.5 font-bold tracking-[-0.01em] text-white', className)}>
      <BrandMark />
      TSA Connect
    </span>
  );
}
