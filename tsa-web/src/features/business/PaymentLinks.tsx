import { useState } from 'react';
import { Outlet, useSearchParams } from 'react-router-dom';
import { Link2, Plus } from 'lucide-react';
import { useMode } from '@/components/shell/mode';
import { Button } from '@/components/ui/button';
import { Segmented } from '@/components/ui/segmented';
import { EmptyState } from '@/components/ui/states';
import { listLinks, listOpenLinks, type OwnMerchant } from './api';
import { BusinessPage } from './BusinessPage';
import { linkColumns, openLinkColumns } from './paymentColumns';
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

// The two kinds of payment link: a fixed amount for one customer, or one address
// many customers pay, each entering the amount (a shop's QR).
const TYPES = [
  { value: '', label: 'One-time' },
  { value: 'reusable', label: 'Reusable' },
] as const;

/** Payment links, made here and shared — no code needed. */
export function PaymentLinks() {
  const [requesting, setRequesting] = useState(false);
  const reusable = useSearchParams()[0].get('type') === 'reusable';
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
          <RequestPaymentDialog open={requesting} onOpenChange={setRequesting} merchant={m} kind={reusable ? 'open' : 'once'} />
        </>
      )}
    </BusinessPage>
  );
}

function LinksBody({ merchant: m, onRequest }: { merchant: OwnMerchant; onRequest: () => void }) {
  const mode = useMode();
  const [params, setParams] = useSearchParams();
  const reusable = params.get('type') === 'reusable';
  const locked = m.status === 'suspended';
  const requestButton = !locked && <Button size="sm" onClick={onRequest}><Plus className="size-4" aria-hidden />Request payment</Button>;
  // A type's filters and page don't carry to the other type.
  const showType = (type: string) => setParams(type ? { type } : {});

  return (
    // One minmax(0, 1fr) column: a wide table scrolls inside its card, not the page.
    <div className="grid grid-cols-1 gap-4">
      {locked && <SuspendedNote>request a payment</SuspendedNote>}
      <Segmented legend="Type of link" name="type" value={reusable ? 'reusable' : ''} options={TYPES} onChange={showType} className="justify-self-start" />
      {reusable ? (
        // Keyed by type: the two lists never share a query, so one type's rows can't
        // show (through the other's columns) while the other loads.
        <PaymentList
          key="open-links"
          kind="open-links"
          rowPath="links/open"
          noun="reusable links"
          load={listOpenLinks}
          columns={openLinkColumns}
          empty={
            <EmptyState
              icon={<Link2 className="size-5" aria-hidden />}
              title="No reusable links yet"
              body="One link many customers pay, each entering the amount — print it as a QR for your shop counter."
              action={requestButton}
            />
          }
        />
      ) : (
        <PaymentList
          key="links"
          kind="links"
          rowPath="links"
          noun="payment links"
          statuses={STATUSES}
          load={listLinks}
          columns={linkColumns}
          searchPlaceholder="What for, customer, reference or ID"
          empty={
            <EmptyState
              icon={<Link2 className="size-5" aria-hidden />}
              title="No payment links yet"
              body={
                mode === 'test'
                  ? 'Make a test link to see what your customer sees — no real money moves.'
                  : 'Ask for any amount and share the link on WhatsApp or as a QR code — no code needed.'
              }
              action={requestButton}
            />
          }
        />
      )}
      <Outlet />
    </div>
  );
}
