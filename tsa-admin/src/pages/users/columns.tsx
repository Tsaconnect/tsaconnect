import type { ColumnDef } from '@tanstack/react-table';
import type { User } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';
import { ROLE_LABELS, VERIFICATION_STATUS_COLORS } from '@/lib/constants';

export const columns: ColumnDef<User>[] = [
  { accessorKey: 'name', header: 'Name' },
  { accessorKey: 'email', header: 'Email' },
  {
    accessorKey: 'role',
    header: 'Role',
    cell: ({ row }) => (
      <span className="text-sm font-medium">{ROLE_LABELS[row.original.role] || row.original.role}</span>
    ),
  },
  {
    accessorKey: 'verificationStatus',
    header: 'Verification',
    cell: ({ row }) => (
      <StatusBadge status={row.original.verificationStatus} colorMap={VERIFICATION_STATUS_COLORS} />
    ),
  },
  {
    accessorKey: 'accountStatus',
    header: 'Status',
    cell: ({ row }) => (
      <StatusBadge
        status={row.original.accountStatus}
        colorMap={{ active: 'bg-green-100 text-green-700', inactive: 'bg-slate-100 text-slate-700', suspended: 'bg-red-100 text-red-700' }}
      />
    ),
  },
  {
    accessorKey: 'createdAt',
    header: 'Joined',
    cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString(),
  },
];
