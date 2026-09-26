import { useEffect, useRef, useState, type FormEvent } from 'react';
import { useQuery, useQueryClient, type QueryClient, type UseQueryResult } from '@tanstack/react-query';
import { Check, Landmark, TriangleAlert, Wallet } from 'lucide-react';
import { ApiError } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Field } from '@/components/ui/field';
import { FormError } from '@/components/ui/form-error';
import { Input, PasswordInput } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { Segmented } from '@/components/ui/segmented';
import { Select } from '@/components/ui/select';
import { Skeleton } from '@/components/ui/skeleton';
import { toast } from '@/components/ui/toaster';
import {
  listBanks, listSettlementOptions, resolveAccount, updateSettlement,
  type Bank, type OwnMerchant, type Pending, type SettlementChange, type SettlementOption,
} from './api';
import { BusinessPage } from './BusinessPage';
import { bankDestination, bankName, cryptoDestination, optionLabel, optionValue } from './display';
import { GuardedActionDialog } from './GuardedActionDialog';
import { SuspendedNote } from './SuspendedNote';
import { useGuardedSubmit } from './useGuardedSubmit';
import { MERCHANT_KEY } from './useMerchant';

const HOUR = 60 * 60_000;
const WALLET = /^0x[0-9a-fA-F]{40}$/;
const ACCOUNT = /^\d{10}$/;
const METHODS = [
  { value: 'fiat', label: 'Bank account' },
  { value: 'crypto', label: 'Crypto wallet (USDC or USDT)' },
] as const;

const resolveQuery = (bankCode: string, accountNumber: string) => ({
  queryKey: ['bank-account', bankCode, accountNumber],
  queryFn: ({ signal }: { signal: AbortSignal }) => resolveAccount(bankCode, accountNumber, signal),
  staleTime: 5 * 60_000,
});

/** The value once it has stopped changing for `ms`. */
function useSettled<T>(value: T, ms: number): T {
  const [settled, setSettled] = useState(value);
  useEffect(() => {
    const t = setTimeout(() => setSettled(value), ms);
    return () => clearTimeout(t);
  }, [value, ms]);
  return settled;
}

type Saved = (change: SettlementChange, result: { merchant?: OwnMerchant; confirmation?: Pending }) => void;

/** The refusal to show; a stale merchant is read again on the way, so a retry is against the fresh one. */
function messageRefreshingIfStale(err: unknown, queryClient: QueryClient): string {
  if (err instanceof ApiError && err.code === 'merchant_stale') void queryClient.invalidateQueries({ queryKey: MERCHANT_KEY });
  return err instanceof Error ? err.message : 'Something went wrong. Please try again.';
}

function Destination({ merchant: m, banks, options }: { merchant: OwnMerchant; banks?: Bank[]; options?: SettlementOption[] }) {
  const [Icon, title, sub] =
    m.settlementMode === 'fiat' ? [Landmark, m.accountName ?? 'Bank account', `${bankDestination(m, banks)} · paid in naira`]
    : m.settlementMode === 'crypto' ? [Wallet, cryptoDestination(m, options), 'Paid in stablecoins to your own wallet']
    : [Landmark, 'Not set up yet', 'Choose a bank account or a crypto wallet below.'];
  return (
    <CardBody className="flex items-center gap-3.5">
      <span aria-hidden className="grid size-11 shrink-0 place-items-center rounded-[10px] bg-accent-muted text-accent-strong">
        <Icon className="size-5" />
      </span>
      <div className="min-w-0">
        <p className="truncate text-body-sm font-semibold">{title}</p>
        <p className="truncate text-caption text-text-secondary">{sub}</p>
      </div>
    </CardBody>
  );
}

function BankForm({ merchant: m, banks, onSaved }: { merchant: OwnMerchant; banks: UseQueryResult<Bank[]>; onSaved: Saved }) {
  const queryClient = useQueryClient();
  const [bankCode, setBankCode] = useState('');
  const [accountNumber, setAccountNumber] = useState('');
  const { password, setPassword, problem, setProblem, busy, run } = useGuardedSubmit();

  // Look the account up once the number is whole and typing has paused.
  const typed = bankCode && ACCOUNT.test(accountNumber) ? `${bankCode}:${accountNumber}` : '';
  const settled = useSettled(typed, 400);
  const lookup = useQuery({ ...resolveQuery(bankCode, accountNumber), enabled: Boolean(typed) && settled === typed, retry: false });

  if (banks.isPending) return <Skeleton className="h-11 w-full" />;
  if (banks.isError) {
    return (
      <Note tone="warning" icon={<TriangleAlert />}>
        {banks.error.message} <button type="button" className="font-semibold underline" onClick={() => void banks.refetch()}>Try again</button>
      </Note>
    );
  }

  function save(e: FormEvent) {
    e.preventDefault();
    if (!bankCode) return setProblem('Choose your bank.');
    if (!ACCOUNT.test(accountNumber)) return setProblem('Enter your 10-digit account number.');
    if (!password) return setProblem('Enter your password.');
    const change: SettlementChange = { settlementMode: 'fiat', bankCode, accountNumber };
    void run(async (pw) => {
      try {
        onSaved(change, await updateSettlement(change, pw, m.version));
      } catch (err) {
        let message = messageRefreshingIfStale(err, queryClient);
        if (err instanceof ApiError && err.code === 'name_mismatch') {
          const name = await queryClient.fetchQuery(resolveQuery(bankCode, accountNumber)).then((r) => r.accountName, () => undefined);
          if (name) message += ` The bank says this account belongs to ${name}.`;
        }
        setProblem(message);
      }
    });
  }

  const bank = bankName(bankCode, banks.data);
  const notFound = lookup.error instanceof ApiError && lookup.error.status === 404;

  return (
    <form onSubmit={save} noValidate className="grid gap-4">
      <div className="grid gap-4 sm:grid-cols-2">
        <Field label="Bank">
          <Select value={bankCode} onChange={(e) => setBankCode(e.target.value)}>
            <option value="">Choose your bank</option>
            {banks.data.map((b) => <option key={b.code} value={b.code}>{b.name}</option>)}
          </Select>
        </Field>
        <Field label="Account number">
          <Input
            inputMode="numeric"
            autoComplete="off"
            value={accountNumber}
            onChange={(e) => setAccountNumber(e.target.value.replace(/\D/g, '').slice(0, 10))}
            className="font-mono"
            placeholder="10 digits"
          />
        </Field>
      </div>
      {typed && (
        <div aria-live="polite">
          {lookup.data ? (
            <Note tone="gold" icon={<Check />}>Account name: <b className="font-semibold">{lookup.data.accountName}</b></Note>
          ) : lookup.isError ? (
            <Note tone="warning" icon={<TriangleAlert />}>
              {notFound ? `${bank ?? 'The bank'} couldn't find this account — check the number.` : "We couldn't check this account — you can still save; we'll check it."}
            </Note>
          ) : (
            <p className="text-caption text-text-secondary">Checking the account…</p>
          )}
        </div>
      )}
      {problem && <FormError>{problem}</FormError>}
      <Field label="Password">
        <PasswordInput autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
      <div><Button type="submit" loading={busy}>Save</Button></div>
    </form>
  );
}

function CryptoForm({ merchant: m, options, onSaved }: { merchant: OwnMerchant; options: UseQueryResult<SettlementOption[]>; onSaved: Saved }) {
  const queryClient = useQueryClient();
  const [token, setToken] = useState('');
  const [wallet, setWallet] = useState('');
  const [walletError, setWalletError] = useState('');
  const walletRef = useRef<HTMLInputElement>(null);
  const { password, setPassword, problem, setProblem, busy, run } = useGuardedSubmit();

  if (options.isPending) return <Skeleton className="h-11 w-full" />;
  if (options.isError) {
    return (
      <Note tone="warning" icon={<TriangleAlert />}>
        We couldn't load the stablecoins you can be paid in. <button type="button" className="font-semibold underline" onClick={() => void options.refetch()}>Try again</button>
      </Note>
    );
  }
  if (options.data.length === 0) {
    return <Note tone="info">Crypto payouts aren't available right now. You can still be paid to a bank account.</Note>;
  }

  function save(e: FormEvent) {
    e.preventDefault();
    const chosen = options.data?.find((o) => optionValue(o) === token);
    const walletOk = WALLET.test(wallet.trim());
    setWalletError(walletOk ? '' : 'Enter a wallet address: 0x followed by 40 letters and numbers.');
    if (!chosen) return setProblem('Choose the stablecoin you want to be paid in.');
    if (!walletOk) {
      setProblem(null);
      walletRef.current?.focus();
      return;
    }
    if (!password) return setProblem('Enter your password.');
    const change: SettlementChange = { settlementMode: 'crypto', chainId: chosen.chainId, tokenAddress: chosen.tokenAddress, walletAddress: wallet.trim() };
    void run(async (pw) => {
      try {
        onSaved(change, await updateSettlement(change, pw, m.version));
      } catch (err) {
        setProblem(messageRefreshingIfStale(err, queryClient));
      }
    });
  }

  return (
    <form onSubmit={save} noValidate className="grid gap-4">
      <Field label="Token" hint="Only these stablecoins, on these chains, can be paid out.">
        <Select value={token} onChange={(e) => setToken(e.target.value)}>
          <option value="">Choose a stablecoin</option>
          {options.data.map((o) => <option key={optionValue(o)} value={optionValue(o)}>{optionLabel(o)}</option>)}
        </Select>
      </Field>
      <Field label="Wallet address" error={walletError || undefined} hint="A wallet you control on that chain — not an exchange deposit address.">
        <Input
          ref={walletRef}
          value={wallet}
          onChange={(e) => setWallet(e.target.value)}
          placeholder="0x…"
          autoComplete="off"
          spellCheck={false}
          className="font-mono"
        />
      </Field>
      {problem && <FormError>{problem}</FormError>}
      <Field label="Password">
        <PasswordInput autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} />
      </Field>
      <div><Button type="submit" loading={busy}>Save</Button></div>
    </form>
  );
}

function PayoutsBody({ merchant: m }: { merchant: OwnMerchant }) {
  const queryClient = useQueryClient();
  const [method, setMethod] = useState<'fiat' | 'crypto'>(m.settlementMode === 'crypto' ? 'crypto' : 'fiat');
  const [waiting, setWaiting] = useState<{ pending: Pending; change: SettlementChange } | null>(null);
  // Bumped after a save, so the form starts clean.
  const [round, setRound] = useState(0);
  const banks = useQuery({
    queryKey: ['banks'], queryFn: ({ signal }) => listBanks(signal), staleTime: HOUR,
    enabled: method === 'fiat' || m.settlementMode === 'fiat',
  });
  const options = useQuery({
    queryKey: ['settlement-options'], queryFn: ({ signal }) => listSettlementOptions(signal), staleTime: HOUR,
    enabled: method === 'crypto' || m.settlementMode === 'crypto',
  });

  const finished = () => {
    toast.success('Payment settings saved');
    setRound((r) => r + 1);
  };

  const saved: Saved = (change, r) => {
    if (r.confirmation) setWaiting({ pending: r.confirmation, change });
    else if (r.merchant) {
      queryClient.setQueryData(MERCHANT_KEY, r.merchant);
      finished();
    }
  };

  return (
    <div className="grid max-w-[820px] gap-4">
      <Card>
        <CardHeader title="Where you're paid" description="Every successful payment is paid out here automatically." />
        <Destination merchant={m} banks={banks.data} options={options.data} />
      </Card>

      {m.status === 'suspended' ? (
        <SuspendedNote>change where you're paid</SuspendedNote>
      ) : (
        <Card>
          <CardHeader title="Change where you're paid" />
          <CardBody className="grid gap-4">
            <Segmented legend="Paid to" name="payout-method" value={method} options={METHODS} onChange={setMethod} className="sm:justify-self-start" />
            {method === 'fiat' ? (
              <BankForm key={`fiat-${round}`} merchant={m} banks={banks} onSaved={saved} />
            ) : (
              <CryptoForm key={`crypto-${round}`} merchant={m} options={options} onSaved={saved} />
            )}
            <p className="text-caption text-text-secondary">
              The account must be in your business name or the name on your verified ID. Every payout change takes effect only after
              you confirm a code we email you. We email you whenever this changes.
            </p>
          </CardBody>
        </Card>
      )}

      {waiting && (
        <GuardedActionDialog
          open
          onOpenChange={(open) => !open && setWaiting(null)}
          title={waiting.change.settlementMode === 'fiat' ? 'Confirm your payout bank account' : 'Confirm your payout wallet'}
          confirmLabel="Confirm"
          initialPending={waiting.pending}
          request={async (password) => {
            const r = await updateSettlement(waiting.change, password, m.version);
            if (!r.confirmation) throw new Error('This change was applied without a code. Reload to see it.');
            return { confirmation: r.confirmation };
          }}
          onDone={() => {
            setWaiting(null);
            finished();
          }}
        />
      )}
    </div>
  );
}

export function Payouts() {
  return <BusinessPage title="Payouts">{(m) => <PayoutsBody merchant={m} />}</BusinessPage>;
}
