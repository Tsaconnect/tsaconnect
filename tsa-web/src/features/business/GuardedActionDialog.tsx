import { useId, useRef, useState, type FormEvent } from 'react';
import { useQueryClient } from '@tanstack/react-query';
import { MailCheck, TriangleAlert } from 'lucide-react';
import { ApiError } from '@/lib/api';
import { Button } from '@/components/ui/button';
import { Dialog } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { FormError } from '@/components/ui/form-error';
import { Input, PasswordInput } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { confirmAction, type ActionResult, type GuardedAnswer, type Pending } from './api';
import { MERCHANT_KEY } from './useMerchant';
import { useGuardedSubmit } from './useGuardedSubmit';

type Props = {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  title: string;
  warning?: string;
  confirmLabel: string;
  danger?: boolean;
  /**
   * Checks the password and emails the code; answers where it went. An answer with
   * no code means nothing needed confirming: it finishes like a confirm.
   */
  request: (password: string) => Promise<GuardedAnswer>;
  onDone: (result: ActionResult) => void;
  /** Already requested (a payout change's 202): open at the code step. */
  initialPending?: Pending;
  /** The merchant changed under this screen and was reloaded. Default: close. */
  onStale?: () => void;
};

type Problem = { message: string; stale?: boolean };

function problemOf(err: unknown): Problem {
  if (!(err instanceof ApiError)) return { message: 'Something went wrong. Please try again.' };
  switch (err.code) {
    case 'password_incorrect':
      return { message: "That password isn't right." };
    case 'account_locked':
      return { message: 'Too many attempts. Your account is locked for a while.' };
    case 'code_invalid':
      return { message: "That code isn't right." };
    case 'no_pending_change':
      return { message: 'This change expired or had too many wrong codes. Start again.' };
    case 'merchant_stale':
      return { message: 'Your settings changed since you opened this — reload and try again.', stale: true };
    default:
      return { message: err.message };
  }
}

/**
 * A change that needs the account password and then a code we email: the one way
 * keys, webhooks, the signing secret and where payouts go are changed.
 * Mounted fresh on every open, so nothing from a previous attempt lingers.
 */
export function GuardedActionDialog(props: Props) {
  return props.open ? <Flow {...props} /> : null;
}

function Flow({ onOpenChange, title, warning, confirmLabel, danger, request, onDone, initialPending, onStale }: Props) {
  const queryClient = useQueryClient();
  const formId = useId();
  const noteId = useId();
  const [pending, setPending] = useState<Pending | null>(initialPending ?? null);
  const [code, setCode] = useState('');
  const { password, setPassword, problem, setProblem, busy, run } = useGuardedSubmit<Problem>();
  const passwordRef = useRef<HTMLInputElement>(null);
  const codeRef = useRef<HTMLInputElement>(null);

  // A result that carries the merchant is used as-is (no second read); a key or
  // secret changes the merchant's version and key prefixes, so read it again.
  const finish = (result: ActionResult) => {
    if (result.merchant) queryClient.setQueryData(MERCHANT_KEY, result.merchant);
    else void queryClient.invalidateQueries({ queryKey: MERCHANT_KEY });
    onDone(result);
  };

  const sendCode = (e: FormEvent) => {
    e.preventDefault();
    if (!password) {
      setProblem({ message: 'Enter your password.' });
      return;
    }
    void run(async (pw) => {
      try {
        const res = await request(pw);
        if (!res.confirmation) return finish({ merchant: res.merchant });
        setPending(res.confirmation);
        setCode('');
      } catch (err) {
        const p = problemOf(err);
        setProblem(p);
        // A wrong password or a lockout: back to the (now empty) field. A stale screen offers Reload instead.
        if (!p.stale) passwordRef.current?.focus();
      }
    });
  };

  const confirm = (e: FormEvent) => {
    e.preventDefault();
    if (!pending) return;
    if (code.length !== 6) {
      setProblem({ message: 'Enter the 6-digit code from your email.' });
      return;
    }
    void run(async () => {
      try {
        finish(await confirmAction(pending.id, code));
      } catch (err) {
        const p = problemOf(err);
        setProblem(p);
        setCode('');
        if (err instanceof ApiError && err.code === 'no_pending_change') setPending(null);
        else codeRef.current?.focus();
      }
    });
  };

  const reload = () => {
    void queryClient.invalidateQueries({ queryKey: MERCHANT_KEY });
    if (onStale) onStale();
    else onOpenChange(false);
  };

  const step = pending ? 'code' : 'password';
  const footer = (
    <>
      <Button variant="secondary" onClick={() => onOpenChange(false)}>Cancel</Button>
      {problem?.stale ? (
        <Button onClick={reload}>Reload</Button>
      ) : step === 'password' ? (
        <Button type="submit" form={formId} loading={busy}>Send code</Button>
      ) : (
        <Button type="submit" form={formId} variant={danger ? 'danger' : 'primary'} loading={busy}>{confirmLabel}</Button>
      )}
    </>
  );

  return (
    <Dialog open onOpenChange={onOpenChange} title={title} footer={footer}>
      {step === 'password' ? (
        <form id={formId} onSubmit={sendCode} noValidate className="grid gap-4">
          {warning && <Note tone="warning" icon={<TriangleAlert />}>{warning}</Note>}
          {problem && <FormError>{problem.message}</FormError>}
          <p className="text-body-sm text-text-secondary">Enter your password. We'll then email you a code to confirm.</p>
          <Field label="Password">
            <PasswordInput ref={passwordRef} autoComplete="current-password" value={password} onChange={(e) => setPassword(e.target.value)} autoFocus />
          </Field>
        </form>
      ) : (
        <form id={formId} onSubmit={confirm} noValidate className="grid gap-4">
          {/* Repeated here: this is the click that makes the change. */}
          {warning && <Note tone="warning" icon={<TriangleAlert />}>{warning}</Note>}
          {problem && <FormError>{problem.message}</FormError>}
          <Note id={noteId} tone="gold" icon={<MailCheck />}>
            We sent a 6-digit code to <b className="font-semibold">{pending!.sentTo}</b>. It expires in 15 minutes.
          </Note>
          <Field label="Code from your email">
            <Input
              ref={codeRef}
              aria-describedby={noteId}
              inputMode="numeric"
              autoComplete="one-time-code"
              value={code}
              onChange={(e) => setCode(e.target.value.replace(/\D/g, '').slice(0, 6))}
              className="font-mono text-[18px] tracking-[0.3em]"
              autoFocus
            />
          </Field>
        </form>
      )}
    </Dialog>
  );
}
