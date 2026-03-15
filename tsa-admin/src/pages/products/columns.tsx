import type { ColumnDef } from '@tanstack/react-table';
import type { Product } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';

const STATUS_COLORS: Record<string, string> = {
  active: 'bg-green-100 text-green-700',
  inactive: 'bg-slate-100 text-slate-700',
  sold_out: 'bg-red-100 text-red-700',
  pending_review: 'bg-amber-100 text-amber-700',
};

export const columns: ColumnDef<Product>[] = [
  { accessorKey: 'name', header: 'Name' },
  {
    accessorKey: 'price',
    header: 'Price',
    cell: ({ row }) => `₦${row.original.price.toLocaleString()}`,
  },
  { accessorKey: 'type', header: 'Type' },
  { accessorKey: 'stock', header: 'Stock' },
  {
    accessorKey: 'status',
    header: 'Status',
    cell: ({ row }) => <StatusBadge status={row.original.status} colorMap={STATUS_COLORS} />,
  },
  {
    accessorKey: 'isFeatured',
    header: 'Featured',
    cell: ({ row }) => row.original.isFeatured ? '⭐' : '—',
  },
  {
    accessorKey: 'createdAt',
    header: 'Created',
    cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString(),
  },
];
