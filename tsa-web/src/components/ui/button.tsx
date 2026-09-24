import { forwardRef, type ButtonHTMLAttributes } from 'react';
import { Slot } from '@radix-ui/react-slot';
import { cva, type VariantProps } from 'class-variance-authority';
import { Loader2 } from 'lucide-react';
import { cn } from '@/lib/cn';

const button = cva(
  'inline-flex select-none items-center justify-center gap-2 whitespace-nowrap rounded-md border border-transparent font-semibold transition-colors duration-150 disabled:cursor-not-allowed disabled:opacity-60',
  {
    variants: {
      variant: {
        primary: 'bg-accent text-on-accent-strong hover:bg-accent-hover disabled:hover:bg-accent',
        secondary: 'border-border bg-surface-alt text-text-primary hover:bg-background-alt hover:border-[color:var(--text-muted)]',
        ghost: 'bg-transparent text-text-primary hover:bg-surface-alt',
        danger: 'border-danger/30 bg-surface text-danger hover:bg-danger-bg',
      },
      size: { md: 'h-10 px-4 text-[14px]', sm: 'h-8 px-3 text-[13px]' },
    },
    defaultVariants: { variant: 'primary', size: 'md' },
  },
);

export type ButtonProps = ButtonHTMLAttributes<HTMLButtonElement> &
  VariantProps<typeof button> & { loading?: boolean; asChild?: boolean };

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(
  ({ className, variant, size, loading, disabled, asChild, children, type, ...props }, ref) => {
    if (asChild) {
      return <Slot ref={ref} className={cn(button({ variant, size }), className)} {...props}>{children}</Slot>;
    }
    return (
      <button
        ref={ref}
        type={type ?? 'button'}
        className={cn(button({ variant, size }), className)}
        disabled={disabled || loading}
        aria-busy={loading || undefined}
        {...props}
      >
        {loading && <Loader2 className="size-4 animate-spin" aria-hidden />}
        {children}
      </button>
    );
  },
);
Button.displayName = 'Button';
