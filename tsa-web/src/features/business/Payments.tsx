import { Outlet } from 'react-router-dom';
import { useMode } from '@/components/shell/mode';
import { listPayments, type PaymentStatus } from './api';
import { BusinessPage } from './BusinessPage';
import { PaymentList } from './PaymentList';
import { NoPaymentsYet } from './PaymentsTable';

const STATUSES: { value: '' | PaymentStatus; label: string }[] = [
  { value: '', label: 'All' },
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'processing', label: 'Processing' },
  { value: 'pending', label: 'Waiting' },
  { value: 'failed', label: 'Failed' },
  { value: 'expired', label: 'Expired' },
];

export function Payments() {
  return <BusinessPage title="Payments">{() => <PaymentsBody />}</BusinessPage>;
}

function PaymentsBody() {
  const mode = useMode();
  return (
    <>
      <PaymentList
        kind="payments"
        noun="payments"
        statuses={STATUSES}
        load={listPayments}
        searchPlaceholder="Reference, description or ID"
        empty={<NoPaymentsYet mode={mode} />}
      />
      <Outlet />
    </>
  );
}
