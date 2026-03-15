import { useParams, useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getOrderById, updateOrderStatus } from '@/api/orders';
import { PageHeader } from '@/components/shared/page-header';
import { StatusBadge } from '@/components/shared/status-badge';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { ORDER_STATUS_COLORS, ORDER_STATUS_LABELS } from '@/lib/constants';
import { Skeleton } from '@/components/ui/skeleton';
import { ArrowLeft } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';

const VALID_TRANSITIONS: Record<string, string[]> = {
  pending: ['processing', 'cancelled'],
  processing: ['shipped', 'cancelled'],
  shipped: ['completed', 'cancelled'],
  completed: [],
  cancelled: [],
};

export default function OrderDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const canUpdate = usePermission('orders.update_status');

  const { data, isLoading } = useQuery({
    queryKey: ['order', id],
    queryFn: () => getOrderById(id!),
    enabled: !!id,
  });

  const mutation = useMutation({
    mutationFn: ({ status }: { status: string }) => updateOrderStatus(id!, status),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['order', id] });
      queryClient.invalidateQueries({ queryKey: ['orders'] });
      toast.success('Order status updated');
    },
  });

  const order = data?.data;
  const nextStatuses = order ? VALID_TRANSITIONS[order.status] || [] : [];

  if (isLoading) return <Skeleton className="h-64 w-full" />;
  if (!order) return <p className="text-slate-500">Order not found.</p>;

  return (
    <div>
      <Button variant="ghost" size="sm" onClick={() => navigate('/orders')} className="mb-4">
        <ArrowLeft className="mr-2 h-4 w-4" /> Back to Orders
      </Button>

      <PageHeader title={`Order ${order.id.slice(0, 8)}...`} />

      <div className="grid grid-cols-2 gap-4 mb-6">
        <Card>
          <CardHeader><CardTitle className="text-sm">Status</CardTitle></CardHeader>
          <CardContent>
            <StatusBadge status={order.status} colorMap={ORDER_STATUS_COLORS} labelMap={ORDER_STATUS_LABELS} />
            {canUpdate && nextStatuses.length > 0 && (
              <div className="mt-3 flex gap-2">
                {nextStatuses.map((s) => (
                  <Button key={s} size="sm" variant={s === 'cancelled' ? 'destructive' : 'default'} onClick={() => mutation.mutate({ status: s })} disabled={mutation.isPending}>
                    {ORDER_STATUS_LABELS[s]}
                  </Button>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
        <Card>
          <CardHeader><CardTitle className="text-sm">Summary</CardTitle></CardHeader>
          <CardContent>
            <p className="text-2xl font-bold">₦{order.total.toLocaleString()}</p>
            <p className="text-xs text-slate-500">{order.currency} · {new Date(order.createdAt).toLocaleDateString()}</p>
          </CardContent>
        </Card>
      </div>

      <Card>
        <CardHeader><CardTitle className="text-sm">Items</CardTitle></CardHeader>
        <CardContent>
          {order.items?.length ? (
            <div className="space-y-2">
              {order.items.map((item, i) => (
                <div key={i} className="flex justify-between border-b border-slate-100 pb-2 last:border-0">
                  <div>
                    <p className="text-sm font-medium">{item.name}</p>
                    <p className="text-xs text-slate-500">Qty: {item.quantity} × ₦{item.unitPrice.toLocaleString()}</p>
                  </div>
                  <p className="text-sm font-medium">₦{item.total.toLocaleString()}</p>
                </div>
              ))}
            </div>
          ) : (
            <p className="text-sm text-slate-500">No items.</p>
          )}
        </CardContent>
      </Card>
    </div>
  );
}
