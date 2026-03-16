import { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getDeposits, updateDepositStatus } from '@/api/deposits';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { StatusBadge } from '@/components/shared/status-badge';
import { DEPOSIT_STATUS_COLORS } from '@/lib/constants';
import { CheckCircle2, XCircle } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { ColumnDef } from '@tanstack/react-table';
import type { Deposit } from '@/types';

const DEPOSIT_STATUS_LABELS: Record<string, string> = {
  pending: 'Pending',
  approved: 'Approved',
  rejected: 'Rejected',
};

export default function DepositsPage() {
  const [page, setPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState('');
  const queryClient = useQueryClient();
  const canApprove = usePermission('deposits.approve');

  const { data, isLoading } = useQuery({
    queryKey: ['deposits', page, statusFilter],
    queryFn: () => getDeposits({ page, limit: 20, status: statusFilter || undefined }),
  });

  const mutation = useMutation({
    mutationFn: ({ id, status }: { id: string; status: 'approved' | 'rejected' }) =>
      updateDepositStatus(id, status),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['deposits'] });
      queryClient.invalidateQueries({ queryKey: ['stats'] });
      toast.success('Deposit status updated');
    },
    onError: () => toast.error('Failed to update deposit'),
  });

  const deposits = data?.data?.deposits ?? [];
  const pagination = data?.data?.pagination;

  const columns: ColumnDef<Deposit>[] = [
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
    ...(canApprove
      ? [
          {
            id: 'actions',
            header: 'Actions',
            cell: ({ row }: { row: { original: Deposit } }) => {
              if (row.original.status !== 'pending') return null;
              return (
                <div className="flex gap-1">
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => mutation.mutate({ id: row.original.id, status: 'approved' })}
                    disabled={mutation.isPending}
                  >
                    <CheckCircle2 className="h-4 w-4 text-green-600" />
                  </Button>
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => mutation.mutate({ id: row.original.id, status: 'rejected' })}
                    disabled={mutation.isPending}
                  >
                    <XCircle className="h-4 w-4 text-red-600" />
                  </Button>
                </div>
              );
            },
          } as ColumnDef<Deposit>,
        ]
      : []),
  ];

  return (
    <div>
      <PageHeader title="Deposits" description="Review and manage deposit requests" />
      <div className="mb-4">
        <Select value={statusFilter} onValueChange={(v) => { setStatusFilter(v === 'all' ? '' : v); setPage(1); }}>
          <SelectTrigger className="w-40"><SelectValue placeholder="All statuses" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="pending">Pending</SelectItem>
            <SelectItem value="approved">Approved</SelectItem>
            <SelectItem value="rejected">Rejected</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DataTable columns={columns} data={deposits} page={page} totalPages={pagination?.totalPages ?? 1} onPageChange={setPage} isLoading={isLoading} />
    </div>
  );
}
