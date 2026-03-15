import type { ColumnDef } from '@tanstack/react-table';
import type { Order } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';
import { ORDER_STATUS_COLORS, ORDER_STATUS_LABELS } from '@/lib/constants';

export const columns: ColumnDef<Order>[] = [
  { accessorKey: 'id', header: 'Order ID', cell: ({ row }) => row.original.id.slice(0, 8) + '...' },
  { accessorKey: 'total', header: 'Total', cell: ({ row }) => `₦${row.original.total.toLocaleString()}` },
  { accessorKey: 'status', header: 'Status', cell: ({ row }) => <StatusBadge status={row.original.status} colorMap={ORDER_STATUS_COLORS} labelMap={ORDER_STATUS_LABELS} /> },
  { accessorKey: 'createdAt', header: 'Date', cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString() },
];
