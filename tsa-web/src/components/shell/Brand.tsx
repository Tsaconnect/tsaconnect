import { cn } from '@/lib/cn';

/** The TSA app icon (tsa-app/assets/images/icon.png), as a rounded tile. */
export function BrandMark({ className }: { className?: string }) {
  return (
    <img
      src="/tsa-mark.png"
      alt=""
      width={28}
      height={28}
      className={cn('size-7 shrink-0 rounded-sm bg-black', className)}
    />
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
