import type { ReactNode } from 'react';
import { Check } from 'lucide-react';
import { Brand } from '@/components/shell/Brand';

/** Sign-in pages: a dark brand panel beside the form; the panel folds away on small screens. */
export function AuthLayout({ heading, lead, points, children }: { heading: string; lead: string; points?: string[]; children: ReactNode }) {
  return (
    <div className="grid min-h-full bg-surface md:grid-cols-2">
      <aside className="hidden flex-col bg-[radial-gradient(1200px_500px_at_-10%_110%,#2B3937_0,var(--slate-900)_60%)] px-10 py-12 text-[#E6EBE9] md:flex lg:px-14">
        <Brand />
        <h2 className="mt-auto mb-3.5 max-w-[460px] text-display font-bold text-white">{heading}</h2>
        <p className="mb-7 max-w-[440px] text-body text-[#A9B4B1]">{lead}</p>
        <ul className="mb-auto grid gap-2.5 text-body-sm text-[#C9D1CF]">
          {points?.map((p) => (
            <li key={p} className="flex items-start gap-2.5">
              <Check className="mt-0.5 size-4 shrink-0 text-gold-bright" strokeWidth={3} aria-hidden />
              {p}
            </li>
          ))}
        </ul>
      </aside>
      <main className="flex flex-col items-center justify-center px-4 py-10 sm:px-6">
        <Brand className="mb-10 self-start text-text-primary md:hidden" />
        <div className="w-full max-w-[380px]">{children}</div>
      </main>
    </div>
  );
}

export function FormError({ children }: { children: ReactNode }) {
  return (
    <div role="alert" className="flex items-start gap-2 rounded-md bg-danger-bg px-3.5 py-2.5 text-[13px] leading-[18px] text-danger-strong">
      {children}
    </div>
  );
}
