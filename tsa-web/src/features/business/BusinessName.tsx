import { useState, type FormEvent } from 'react';
import { Clock } from 'lucide-react';
import { ApiError } from '@/lib/api';
import { relative } from '@/lib/format';
import { Button } from '@/components/ui/button';
import { Card, CardBody, CardHeader } from '@/components/ui/card';
import { Dialog } from '@/components/ui/dialog';
import { Field } from '@/components/ui/field';
import { Input } from '@/components/ui/input';
import { Note } from '@/components/ui/note';
import { toast } from '@/components/ui/toaster';
import { requestBusinessName, type OwnMerchant } from './api';
import { GuardedActionDialog } from './GuardedActionDialog';

// The API's bounds (pay_business_name.go); hidden characters and reserved words are its call alone.
const MIN_NAME = 2;
const MAX_NAME = 100;

/** Spacing as the API stores it: trimmed, every run of whitespace one space. */
const tidy = (name: string) => name.trim().split(/\s+/).filter(Boolean).join(' ');

/**
 * An uncoded 422 is the API refusing the name itself (hidden characters, a
 * reserved word, the length). Its message comes as "invalid merchant settings: …".
 */
function nameRefusal(err: unknown): string | null {
  if (!(err instanceof ApiError) || err.status !== 422 || err.code) return null;
  const why = err.message.replace(/^invalid merchant settings:\s*/i, '');
  return why.charAt(0).toUpperCase() + why.slice(1);
}

/**
 * The business name, and the owner's way to ask for a new one. A rename is
 * guarded (password, then an emailed code) and takes effect only when TSA
 * Connect approves it: customers and payouts keep the current name until then.
 */
export function BusinessName({ merchant: m }: { merchant: OwnMerchant }) {
  const [editing, setEditing] = useState(false);
  const [confirming, setConfirming] = useState(false);
  const [name, setName] = useState('');
  const [error, setError] = useState('');
  const locked = m.status === 'suspended';
  // Renames can be asked for only while the feature is switched on (the API says so).
  const canRename = !locked && m.businessNameChangeEnabled === true;
  const pending = m.pendingBusinessName;

  const startEdit = () => {
    setName(pending ?? m.businessName);
    setError('');
    setEditing(true);
  };

  const continueEdit = (e: FormEvent) => {
    e.preventDefault();
    const next = tidy(name);
    // Counted in characters, as the API does: an emoji is one, not two.
    const length = [...next].length;
    if (length < MIN_NAME || length > MAX_NAME) return setError(`Enter a name of ${MIN_NAME} to ${MAX_NAME} characters.`);
    if (next === pending) return setError('That name is already waiting for approval.');
    // Asking for the current name back is how a waiting rename is withdrawn; with none waiting it would change nothing.
    if (next === m.businessName && !pending) return setError("That's already your business name.");
    setName(next);
    setError('');
    setEditing(false);
    setConfirming(true);
  };

  // Cancelling the code step, or reloading a stale screen, goes back to the form with what was typed.
  const backToForm = (why = '') => {
    setError(why);
    setConfirming(false);
    setEditing(true);
  };

  const request = async (password: string) => {
    try {
      return await requestBusinessName(name, password, m.version);
    } catch (err) {
      // A name the API won't take is shown on the name field, not under the password.
      const why = nameRefusal(err);
      if (why) backToForm(why);
      throw err;
    }
  };

  const withdrawing = name === m.businessName;

  const done = () => {
    setConfirming(false);
    toast.success(withdrawing ? 'Rename request withdrawn' : 'Sent for approval');
  };

  return (
    <section aria-label="Business name">
      <Card>
        <CardHeader
          title="Business name"
          description="Customers see it at checkout, on payment links and on receipts. Payout accounts must be in this name or yours."
          actions={canRename && <Button size="sm" variant="secondary" onClick={startEdit}>Change business name</Button>}
        />
        <CardBody className="grid gap-3">
          <p className="text-body-sm font-semibold break-words">{m.businessName}</p>
          {pending && (
            <Note tone="info" icon={<Clock />}>
              Your new name '{pending}' is waiting for approval. Customers and payouts keep '{m.businessName}' until then.
              {m.pendingSince && <span className="block">Asked {relative(m.pendingSince)}.</span>}
            </Note>
          )}
        </CardBody>
      </Card>

      <Dialog
        open={editing}
        onOpenChange={setEditing}
        title="Change your business name"
        focusFirstField
        footer={
          <>
            <Button variant="secondary" onClick={() => setEditing(false)}>Cancel</Button>
            <Button type="submit" form="business-name">Continue</Button>
          </>
        }
      >
        <form id="business-name" onSubmit={continueEdit} noValidate className="grid gap-4">
          <Field label="New business name" error={error || undefined}>
            <Input value={name} maxLength={MAX_NAME} autoComplete="organization" onChange={(e) => setName(e.target.value)} />
          </Field>
          <p className="text-caption text-text-secondary">
            We check every new name before it's used. Until we approve it, customers and payouts keep {m.businessName}.
            {pending && ` Asking for ${m.businessName} withdraws the name waiting for approval.`}
          </p>
        </form>
      </Dialog>

      <GuardedActionDialog
        open={confirming}
        onOpenChange={(open) => !open && backToForm()}
        onStale={() => backToForm()}
        title={withdrawing ? 'Keep your business name' : 'Confirm your new business name'}
        warning={withdrawing ? undefined : `We'll ask for your business to be renamed "${name}". It changes only when we approve it.`}
        confirmLabel="Send for approval"
        request={request}
        onDone={done}
      />
    </section>
  );
}
