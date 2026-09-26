import { useState } from 'react';
import { Outlet } from 'react-router-dom';
import { Link2, Plus } from 'lucide-react';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { EmptyState } from '@/components/ui/states';
import { listLinks, type OwnMerchant } from './api';
import { BusinessPage } from './BusinessPage';
import { linkColumns } from './paymentColumns';
import { PaymentList } from './PaymentList';
import { RequestPaymentDialog } from './RequestPaymentDialog';
import { SuspendedNote } from './SuspendedNote';
import { useMerchant } from './useMerchant';

// Cancelled is its own filter: the API keeps a cancelled link as expired, and
// narrows `status=cancelled` to the ones the owner closed.
const STATUSES = [
  { value: '', label: 'All' },
  { value: 'pending', label: 'Awaiting payment' },
  { value: 'succeeded', label: 'Paid' },
  { value: 'expired', label: 'Expired' },
  { value: 'cancelled', label: 'Cancelled' },
] as const;

/** A payment for any amount, made here and shared as a link — no code needed. */
export function PaymentLinks() {
  const [requesting, setRequesting] = useState(false);
  // A suspended merchant can't take payments, so it can't ask for one either.
  const locked = useMerchant().data?.status === 'suspended';
  const request = (
    <Button size="sm" onClick={() => setRequesting(true)} disabled={locked}>
      <Plus className="size-4" aria-hidden />Request payment
    </Button>
  );
  return (
    <BusinessPage title="Payment links" actions={request}>
      {(m) => (
        <>
          <LinksBody merchant={m} onRequest={() => setRequesting(true)} />
          <RequestPaymentDialog open={requesting} onOpenChange={setRequesting} merchant={m} />
        </>
      )}
    </BusinessPage>
  );
}

function LinksBody({ merchant: m, onRequest }: { merchant: OwnMerchant; onRequest: () => void }) {
  const mode = useMode();
  const locked = m.status === 'suspended';
  return (
    // One minmax(0, 1fr) column: a wide table scrolls inside its card, not the page.
    <div className="grid grid-cols-1 gap-4">
      {locked && <SuspendedNote>request a payment</SuspendedNote>}
      <PaymentList
        kind="links"
        noun="payment links"
        statuses={STATUSES}
        load={listLinks}
        columns={linkColumns}
        searchPlaceholder="What for, reference or ID"
        empty={
          <EmptyState
            icon={<Link2 className="size-5" aria-hidden />}
            title="No payment links yet"
            body={
              mode === 'test'
                ? 'Make a test link to see what your customer sees — no real money moves.'
                : 'Ask for any amount and share the link on WhatsApp or as a QR code — no code needed.'
            }
            action={!locked && <Button size="sm" onClick={onRequest}><Plus className="size-4" aria-hidden />Request payment</Button>}
          />
        }
      />
      <Outlet />
    </div>
  );
}
