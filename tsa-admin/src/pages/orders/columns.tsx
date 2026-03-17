import type { ColumnDef } from '@tanstack/react-table';
import type { Order } from '@/types';
import { StatusBadge } from '@/components/shared/status-badge';
import { ORDER_STATUS_COLORS, ORDER_STATUS_LABELS } from '@/lib/constants';

function formatWei(weiStr: string, token: string): string {
  if (!weiStr) return '0.00';
  try {
    const decimals = token === 'MCGP' ? 18 : 6;
    const value = BigInt(weiStr);
    const divisor = BigInt(10 ** decimals);
    const whole = value / divisor;
    const frac = value % divisor;
    const fracStr = frac.toString().padStart(decimals, '0').slice(0, token === 'MCGP' ? 4 : 2);
    return `${whole}.${fracStr}`;
  } catch {
    return '0.00';
  }
}

const SHIPPING_ZONE_LABELS: Record<string, string> = {
  same_city: 'Same City',
  same_state: 'Same State',
  same_country: 'Same Country',
  international: 'International',
};

export { formatWei, SHIPPING_ZONE_LABELS };

export const columns: ColumnDef<Order>[] = [
  { accessorKey: 'id', header: 'Order ID', cell: ({ row }) => row.original.id.slice(0, 8) + '...' },
  {
    accessorKey: 'token', header: 'Token', cell: ({ row }) => (
      <span className="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-700">
        {row.original.token}
      </span>
    ),
  },
  {
    accessorKey: 'totalAmount', header: 'Total', cell: ({ row }) =>
      `${formatWei(row.original.totalAmount, row.original.token)} ${row.original.token}`,
  },
  {
    accessorKey: 'status', header: 'Status', cell: ({ row }) =>
      <StatusBadge status={row.original.status} colorMap={ORDER_STATUS_COLORS} labelMap={ORDER_STATUS_LABELS} />,
  },
  {
    accessorKey: 'shippingZone', header: 'Shipping Zone', cell: ({ row }) =>
      SHIPPING_ZONE_LABELS[row.original.shippingZone] || row.original.shippingZone,
  },
  {
    accessorKey: 'createdAt', header: 'Date', cell: ({ row }) =>
      new Date(row.original.createdAt).toLocaleDateString(),
  },
];
