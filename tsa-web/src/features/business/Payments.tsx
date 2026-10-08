import { Outlet } from 'react-router-dom';
import { useMode } from '@/components/shell/mode';
import { listPayments, type PaymentStatus } from './api';
import { BusinessPage } from './BusinessPage';
import { paymentColumns } from './paymentColumns';
import { PaymentList } from './PaymentList';
import { NoPaymentsYet } from './PaymentsTable';

// Cancelled: payment links their owner called off (the API keeps them apart from Expired).
const STATUSES: { value: '' | PaymentStatus | 'cancelled'; label: string }[] = [
  { value: '', label: 'All' },
  { value: 'succeeded', label: 'Succeeded' },
  { value: 'processing', label: 'Processing' },
  { value: 'pending', label: 'Waiting' },
  { value: 'failed', label: 'Failed' },
  { value: 'expired', label: 'Expired' },
  { value: 'cancelled', label: 'Cancelled' },
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
        rowPath="payments"
        noun="payments"
        columns={paymentColumns}
        statuses={STATUSES}
        load={listPayments}
        searchPlaceholder="Reference, description or ID"
        dateFilter
        empty={<NoPaymentsYet mode={mode} />}
      />
      <Outlet />
    </>
  );
}
