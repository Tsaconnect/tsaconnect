import type { ComponentType, ReactNode } from 'react';
import { Link, NavLink } from 'react-router-dom';
import { BookOpen, Coins, CreditCard, KeyRound, Landmark, LayoutGrid, Link2, LogOut, ReceiptText, UserRound } from 'lucide-react';
import { useSession } from '@/session/context';
import { initials } from '@/lib/format';
import { cn } from '@/lib/cn';
import { SegmentedNav } from '@/components/ui/segmented';
import { Skeleton } from '@/components/ui/skeleton';
import { Brand } from './Brand';
import type { Mode } from './mode';

export type ShellSection = 'personal' | 'business';
/** What the sidebar shows of the TSA Pay merchant (a subset of the owner view). */
export type ShellMerchant = { businessName: string; settlementMode: 'fiat' | 'crypto' | '' };

const item = 'flex items-center gap-2.5 rounded-sm px-2.5 py-2 text-[13.5px] font-medium text-on-slate-muted transition-colors hover:bg-slate-elevated hover:text-white';
const active = 'bg-slate-line text-white shadow-[inset_2px_0_0_var(--gold-bright)] hover:bg-slate-line';

function Item({ to, icon: Icon, children, onNavigate }: { to: string; icon: ComponentType<{ className?: string }>; children: ReactNode; onNavigate?: () => void }) {
  return (
    <NavLink to={to} onClick={onNavigate} className={({ isActive }) => cn(item, isActive && active)}>
      <Icon className="size-[17px] shrink-0 opacity-85" aria-hidden />
      {children}
    </NavLink>
  );
}

const Heading = ({ children }: { children: ReactNode }) => (
  <p className="px-2.5 pt-3.5 pb-1.5 text-overline font-semibold uppercase tracking-[.08em] text-text-muted">{children}</p>
);

function MerchantCard({ merchant }: { merchant: ShellMerchant | null | undefined }) {
  if (merchant === null) return null;
  return (
    <div className="mb-[18px] flex items-center gap-2.5 rounded-md border border-slate-raised bg-slate-elevated p-2.5">
      {merchant === undefined ? (
        <>
          <Skeleton className="size-[30px] rounded-sm bg-slate-raised" />
          <div className="grid flex-1 gap-1.5">
            <Skeleton className="h-3 w-24 bg-slate-raised" />
            <Skeleton className="h-2.5 w-32 bg-slate-raised" />
          </div>
        </>
      ) : (
        <>
          <span aria-hidden className="grid size-[30px] shrink-0 place-items-center rounded-sm bg-slate-raised text-caption font-bold text-gold-bright">
            {initials(merchant.businessName)}
          </span>
          <div className="min-w-0">
            <p className="truncate text-[13px] font-semibold text-white">{merchant.businessName}</p>
            <p className="truncate text-[11.5px] text-text-muted">
              TSA Pay · paid in {merchant.settlementMode === 'crypto' ? 'crypto' : 'naira'}
            </p>
          </div>
        </>
      )}
    </div>
  );
}

export function Sidebar({ section, mode, merchant, onNavigate }: {
  section: ShellSection;
  mode: Mode;
  merchant?: ShellMerchant | null;
  onNavigate?: () => void;
}) {
  const { user, signOut } = useSession();
  const biz = (page: string) => `/business/${mode}/${page}`;

  return (
    <div className="flex h-full flex-col overflow-y-auto bg-slate-900 px-3.5 py-[18px] text-on-slate-muted">
      <Link to={biz('overview')} onClick={onNavigate} className="mb-[18px] self-start rounded-sm px-2 pt-1">
        <Brand />
      </Link>

      <SegmentedNav
        label="Workspace"
        look="slate"
        className="mb-3"
        items={[
          { to: '/account', label: 'Personal', current: section === 'personal', onClick: onNavigate },
          { to: biz('overview'), label: 'Business', current: section === 'business', onClick: onNavigate },
        ]}
      />

      {section === 'business' && <MerchantCard merchant={merchant} />}

      <nav aria-label="Main" className="grid gap-0.5">
        {section === 'business' ? (
          <>
            <Item to={biz('overview')} icon={LayoutGrid} onNavigate={onNavigate}>Overview</Item>
            <Item to={biz('payments')} icon={CreditCard} onNavigate={onNavigate}>Payments</Item>
            <Item to={biz('links')} icon={Link2} onNavigate={onNavigate}>Payment links</Item>
            <Heading>Business</Heading>
            <Item to={biz('payouts')} icon={Landmark} onNavigate={onNavigate}>Payouts</Item>
            <Item to={biz('reconciliation')} icon={ReceiptText} onNavigate={onNavigate}>Refunds &amp; reconciliation</Item>
            <Item to={biz('liquidity')} icon={Coins} onNavigate={onNavigate}>Liquidity earnings</Item>
            <Heading>Developers</Heading>
            <Item to={biz('developers')} icon={KeyRound} onNavigate={onNavigate}>API keys &amp; webhooks</Item>
            <Item to="/docs" icon={BookOpen} onNavigate={onNavigate}>Docs</Item>
          </>
        ) : (
          <Item to="/account" icon={UserRound} onNavigate={onNavigate}>Account</Item>
        )}
      </nav>

      <div className="mt-auto flex items-center gap-2.5 border-t border-slate-line pt-3">
        <span aria-hidden className="grid size-8 shrink-0 place-items-center rounded-full bg-gold-100 text-caption font-bold text-accent-strong">
          {initials(user?.name ?? '')}
        </span>
        <div className="min-w-0 flex-1 leading-tight">
          <p className="truncate text-[12.5px] font-semibold text-white">{user?.name}</p>
          <p className="truncate text-[11.5px] text-text-muted">{user?.email}</p>
        </div>
        <button
          type="button"
          onClick={() => void signOut()}
          aria-label="Sign out"
          title="Sign out"
          className="grid size-8 shrink-0 place-items-center rounded-sm text-text-muted transition-colors hover:bg-slate-elevated hover:text-white"
        >
          <LogOut className="size-4" aria-hidden />
        </button>
      </div>
    </div>
  );
}
