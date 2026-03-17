import { useState } from 'react';
import { useParams, useNavigate } from 'react-router-dom';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getOrderById, resolveDispute } from '@/api/orders';
import { PageHeader } from '@/components/shared/page-header';
import { StatusBadge } from '@/components/shared/status-badge';
import { ConfirmDialog } from '@/components/shared/confirm-dialog';
import { Button } from '@/components/ui/button';
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card';
import { Separator } from '@/components/ui/separator';
import { ORDER_STATUS_COLORS, ORDER_STATUS_LABELS } from '@/lib/constants';
import { Skeleton } from '@/components/ui/skeleton';
import { ArrowLeft, ExternalLink, Copy, Clock } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import { formatWei, SHIPPING_ZONE_LABELS } from './columns';

const SONIC_EXPLORER = 'https://testnet.sonicscan.org/tx';

function truncateHex(hex: string) {
  if (!hex) return '';
  if (hex.length <= 16) return hex;
  return `${hex.slice(0, 10)}...${hex.slice(-6)}`;
}

function copyToClipboard(text: string) {
  navigator.clipboard.writeText(text);
  toast.success('Copied to clipboard');
}

function EscrowCountdown({ expiresAt }: { expiresAt: string }) {
  const expires = new Date(expiresAt);
  const now = new Date();
  const diffMs = expires.getTime() - now.getTime();
  if (diffMs <= 0) return <span className="text-red-600 text-sm font-medium">Expired</span>;
  const days = Math.floor(diffMs / (1000 * 60 * 60 * 24));
  const hours = Math.floor((diffMs % (1000 * 60 * 60 * 24)) / (1000 * 60 * 60));
  return (
    <span className="inline-flex items-center gap-1 text-sm text-amber-700">
      <Clock className="h-3.5 w-3.5" />
      Expires in {days > 0 ? `${days}d ` : ''}{hours}h
    </span>
  );
}

function TxLink({ hash }: { hash: string }) {
  return (
    <div className="flex items-center gap-2">
      <span className="font-mono text-sm">{truncateHex(hash)}</span>
      <button onClick={() => copyToClipboard(hash)} className="text-slate-400 hover:text-slate-600">
        <Copy className="h-3.5 w-3.5" />
      </button>
      <a href={`${SONIC_EXPLORER}/${hash}`} target="_blank" rel="noopener noreferrer" className="text-blue-500 hover:text-blue-700">
        <ExternalLink className="h-3.5 w-3.5" />
      </a>
    </div>
  );
}

function InfoRow({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex justify-between py-1.5">
      <span className="text-sm text-slate-500">{label}</span>
      <span className="text-sm font-medium">{children}</span>
    </div>
  );
}

export default function OrderDetailPage() {
  const { id } = useParams<{ id: string }>();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const canResolve = usePermission('orders.resolve_dispute');

  const [confirmAction, setConfirmAction] = useState<'refund' | 'release' | null>(null);

  const { data, isLoading } = useQuery({
    queryKey: ['order', id],
    queryFn: () => getOrderById(id!),
    enabled: !!id,
  });

  const mutation = useMutation({
    mutationFn: (refundBuyer: boolean) => resolveDispute(id!, refundBuyer),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['order', id] });
      queryClient.invalidateQueries({ queryKey: ['orders'] });
      toast.success(confirmAction === 'refund' ? 'Buyer refunded successfully' : 'Funds released to seller');
      setConfirmAction(null);
    },
    onError: () => {
      toast.error('Failed to resolve dispute');
    },
  });

  const order = data?.data;

  if (isLoading) return <Skeleton className="h-64 w-full" />;
  if (!order) return <p className="text-slate-500">Order not found.</p>;

  return (
    <div>
      <Button variant="ghost" size="sm" onClick={() => navigate('/orders')} className="mb-4">
        <ArrowLeft className="mr-2 h-4 w-4" /> Back to Orders
      </Button>

      <PageHeader title={`Order ${order.id.slice(0, 8)}...`} />

      <div className="grid grid-cols-1 md:grid-cols-2 gap-4 mb-6">
        {/* Status Card */}
        <Card>
          <CardHeader><CardTitle className="text-sm">Status</CardTitle></CardHeader>
          <CardContent>
            <div className="flex items-center gap-3">
              <StatusBadge status={order.status} colorMap={ORDER_STATUS_COLORS} labelMap={ORDER_STATUS_LABELS} />
              {order.status === 'escrowed' && order.escrowExpiresAt && (
                <EscrowCountdown expiresAt={order.escrowExpiresAt} />
              )}
            </div>
          </CardContent>
        </Card>

        {/* Order Info Card */}
        <Card>
          <CardHeader><CardTitle className="text-sm">Order Info</CardTitle></CardHeader>
          <CardContent>
            <InfoRow label="Product ID">{order.productId.slice(0, 8)}...</InfoRow>
            <InfoRow label="Quantity">{order.quantity}</InfoRow>
            <InfoRow label="Token">
              <span className="inline-flex items-center rounded-full bg-slate-100 px-2 py-0.5 text-xs font-medium text-slate-700">
                {order.token}
              </span>
            </InfoRow>
            <InfoRow label="Shipping Zone">{SHIPPING_ZONE_LABELS[order.shippingZone] || order.shippingZone}</InfoRow>
          </CardContent>
        </Card>

        {/* Amounts Card */}
        <Card>
          <CardHeader><CardTitle className="text-sm">Amounts</CardTitle></CardHeader>
          <CardContent>
            <InfoRow label="Product Amount">{formatWei(order.productAmount, order.token)} {order.token}</InfoRow>
            <InfoRow label="Shipping">{formatWei(order.shippingAmount, order.token)} {order.token}</InfoRow>
            <InfoRow label="Platform Fee">{formatWei(order.platformFee, order.token)} {order.token}</InfoRow>
            <Separator className="my-2" />
            <InfoRow label="Total">
              <span className="font-bold">{formatWei(order.totalAmount, order.token)} {order.token}</span>
            </InfoRow>
          </CardContent>
        </Card>

        {/* Parties Card */}
        <Card>
          <CardHeader><CardTitle className="text-sm">Parties</CardTitle></CardHeader>
          <CardContent>
            <InfoRow label="Buyer ID">{order.buyerId.slice(0, 8)}...</InfoRow>
            <InfoRow label="Seller ID">{order.sellerId.slice(0, 8)}...</InfoRow>
            {order.buyerUpline && <InfoRow label="Buyer Upline">{order.buyerUpline.slice(0, 10)}...</InfoRow>}
          </CardContent>
        </Card>

        {/* Blockchain Card */}
        {(order.contractOrderId || order.escrowTxHash || order.approveTxHash || order.releaseTxHash) && (
          <Card>
            <CardHeader><CardTitle className="text-sm">Blockchain</CardTitle></CardHeader>
            <CardContent className="space-y-2">
              {order.contractOrderId && (
                <div>
                  <p className="text-xs text-slate-500 mb-1">Contract Order ID</p>
                  <div className="flex items-center gap-2">
                    <span className="font-mono text-sm">{truncateHex(order.contractOrderId)}</span>
                    <button onClick={() => copyToClipboard(order.contractOrderId!)} className="text-slate-400 hover:text-slate-600">
                      <Copy className="h-3.5 w-3.5" />
                    </button>
                  </div>
                </div>
              )}
              {order.approveTxHash && (
                <div>
                  <p className="text-xs text-slate-500 mb-1">Approve Tx</p>
                  <TxLink hash={order.approveTxHash} />
                </div>
              )}
              {order.escrowTxHash && (
                <div>
                  <p className="text-xs text-slate-500 mb-1">Escrow Tx</p>
                  <TxLink hash={order.escrowTxHash} />
                </div>
              )}
              {order.releaseTxHash && (
                <div>
                  <p className="text-xs text-slate-500 mb-1">Release Tx</p>
                  <TxLink hash={order.releaseTxHash} />
                </div>
              )}
            </CardContent>
          </Card>
        )}

        {/* Timeline Card */}
        <Card>
          <CardHeader><CardTitle className="text-sm">Timeline</CardTitle></CardHeader>
          <CardContent className="space-y-2">
            <InfoRow label="Created">{new Date(order.createdAt).toLocaleString()}</InfoRow>
            {order.sellerDeliveredAt && (
              <div>
                <InfoRow label="Seller Delivered">{new Date(order.sellerDeliveredAt).toLocaleString()}</InfoRow>
                {order.deliveryProofUrl && (
                  <a href={order.deliveryProofUrl} target="_blank" rel="noopener noreferrer" className="text-xs text-blue-500 hover:text-blue-700 inline-flex items-center gap-1">
                    View delivery proof <ExternalLink className="h-3 w-3" />
                  </a>
                )}
              </div>
            )}
            {order.buyerConfirmedAt && (
              <InfoRow label="Buyer Confirmed">{new Date(order.buyerConfirmedAt).toLocaleString()}</InfoRow>
            )}
            {order.escrowExpiresAt && (
              <InfoRow label="Escrow Expires">{new Date(order.escrowExpiresAt).toLocaleString()}</InfoRow>
            )}
          </CardContent>
        </Card>

        {/* Notes */}
        {order.notes && (
          <Card>
            <CardHeader><CardTitle className="text-sm">Notes</CardTitle></CardHeader>
            <CardContent>
              <p className="text-sm text-slate-600">{order.notes}</p>
            </CardContent>
          </Card>
        )}

        {/* Actions Card — Dispute Resolution */}
        {canResolve && order.status === 'refund_requested' && (
          <Card>
            <CardHeader><CardTitle className="text-sm">Resolve Dispute</CardTitle></CardHeader>
            <CardContent>
              <p className="text-sm text-slate-500 mb-3">
                The buyer has requested a refund. Review the order details and choose an action.
              </p>
              <div className="flex gap-2">
                <Button variant="destructive" size="sm" onClick={() => setConfirmAction('refund')}>
                  Refund Buyer
                </Button>
                <Button variant="default" size="sm" onClick={() => setConfirmAction('release')}>
                  Release to Seller
                </Button>
              </div>
            </CardContent>
          </Card>
        )}
      </div>

      {/* Confirm Dialogs */}
      <ConfirmDialog
        open={confirmAction === 'refund'}
        onOpenChange={(open) => !open && setConfirmAction(null)}
        title="Refund Buyer"
        description="This will refund the escrowed funds to the buyer. This action cannot be undone."
        confirmLabel="Refund Buyer"
        variant="destructive"
        loading={mutation.isPending}
        onConfirm={() => mutation.mutate(true)}
      />
      <ConfirmDialog
        open={confirmAction === 'release'}
        onOpenChange={(open) => !open && setConfirmAction(null)}
        title="Release to Seller"
        description="This will release the escrowed funds to the seller. This action cannot be undone."
        confirmLabel="Release to Seller"
        loading={mutation.isPending}
        onConfirm={() => mutation.mutate(false)}
      />
    </div>
  );
}
