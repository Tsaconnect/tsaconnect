import type { ColumnDef } from '@tanstack/react-table';
import type { Deposit } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';
import { DEPOSIT_STATUS_COLORS } from '@/lib/constants';

const DEPOSIT_STATUS_LABELS: Record<string, string> = {
  pending: 'Pending',
  approved: 'Approved',
  rejected: 'Rejected',
};

export const columns: ColumnDef<Deposit>[] = [
  { accessorKey: 'id', header: 'Deposit ID', cell: ({ row }) => row.original.id.slice(0, 8) + '...' },
  { accessorKey: 'amount', header: 'Amount', cell: ({ row }) => `₦${row.original.amount.toLocaleString()}` },
  { accessorKey: 'currency', header: 'Currency' },
  {
    accessorKey: 'status',
    header: 'Status',
    cell: ({ row }) => (
      <StatusBadge
        status={row.original.status}
        colorMap={DEPOSIT_STATUS_COLORS}
        labelMap={DEPOSIT_STATUS_LABELS}
      />
    ),
  },
  { accessorKey: 'createdAt', header: 'Date', cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString() },
];
