import { useState } from 'react';
import { useQuery } from '@tanstack/react-query';
import { useNavigate } from 'react-router-dom';
import { getAdminOrders } from '@/api/orders';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { Input } from '@/components/ui/input';
import { columns } from './columns';

export default function OrdersPage() {
  const [page, setPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState('');
  const [search, setSearch] = useState('');
  const navigate = useNavigate();

  const { data, isLoading } = useQuery({
    queryKey: ['orders', page, statusFilter, search],
    queryFn: () => getAdminOrders({ page, limit: 20, status: statusFilter || undefined, search: search || undefined }),
  });

  const orders = data?.data?.orders ?? [];
  const pagination = data?.data?.pagination;

  return (
    <div>
      <PageHeader title="Orders" description="View and manage all orders" />
      <div className="mb-4 flex gap-3">
        <Input
          placeholder="Search by order ID..."
          value={search}
          onChange={(e) => { setSearch(e.target.value); setPage(1); }}
          className="w-64"
        />
        <Select value={statusFilter} onValueChange={(v) => { setStatusFilter(v === 'all' ? '' : v); setPage(1); }}>
          <SelectTrigger className="w-48"><SelectValue placeholder="All statuses" /></SelectTrigger>
          <SelectContent>
            <SelectItem value="all">All statuses</SelectItem>
            <SelectItem value="pending_payment">Pending Payment</SelectItem>
            <SelectItem value="escrowed">Escrowed</SelectItem>
            <SelectItem value="delivered">Delivered</SelectItem>
            <SelectItem value="completed">Completed</SelectItem>
            <SelectItem value="refund_requested">Refund Requested</SelectItem>
            <SelectItem value="refunded">Refunded</SelectItem>
            <SelectItem value="cancelled">Cancelled</SelectItem>
          </SelectContent>
        </Select>
      </div>
      <DataTable columns={columns} data={orders} page={page} totalPages={pagination?.totalPages ?? 1} onPageChange={setPage} isLoading={isLoading} onRowClick={(row) => navigate(`/orders/${row.id}`)} />
    </div>
  );
}
