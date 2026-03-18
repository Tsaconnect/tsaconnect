import React, { useState } from 'react';
import { useQuery, useMutation, useQueryClient } from '@tanstack/react-query';
import { getMerchantRequests, approveMerchantRequest, rejectMerchantRequest } from '@/api/merchant-requests';
import { DataTable } from '@/components/shared/data-table';
import { PageHeader } from '@/components/shared/page-header';
import { Button } from '@/components/ui/button';
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from '@/components/ui/select';
import { StatusBadge } from '@/components/shared/status-badge';
import { Dialog, DialogContent, DialogHeader, DialogTitle, DialogFooter } from '@/components/ui/dialog';
import { Textarea } from '@/components/ui/textarea';
import { MERCHANT_REQUEST_STATUS_COLORS, MERCHANT_REQUEST_STATUS_LABELS, BUSINESS_TYPE_LABELS } from '@/lib/constants';
import { CheckCircle2, XCircle, Eye } from 'lucide-react';
import { toast } from 'sonner';
import { usePermission } from '@/hooks/use-permission';
import type { ColumnDef } from '@tanstack/react-table';
import type { MerchantRequest } from '@/types';

export default function MerchantRequestsPage() {
  const [page, setPage] = useState(1);
  const [statusFilter, setStatusFilter] = useState('');
  const [selected, setSelected] = useState<MerchantRequest | null>(null);
  const [rejectNote, setRejectNote] = useState('');
  const [approveNote, setApproveNote] = useState('');
  const [showDetail, setShowDetail] = useState(false);
  const [showReject, setShowReject] = useState(false);
  const [showApprove, setShowApprove] = useState(false);
  const queryClient = useQueryClient();
  const canApprove = usePermission('merchant_requests.approve');

  const { data, isLoading } = useQuery({
    queryKey: ['merchant-requests', page, statusFilter],
    queryFn: () => getMerchantRequests({ page, limit: 20, status: statusFilter || undefined }),
  });

  const approveMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note?: string }) => approveMerchantRequest(id, note),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['merchant-requests'] });
      toast.success('Merchant request approved');
      setShowApprove(false);
      setApproveNote('');
    },
    onError: () => toast.error('Failed to approve request'),
  });

  const rejectMutation = useMutation({
    mutationFn: ({ id, note }: { id: string; note: string }) => rejectMerchantRequest(id, note),
    onSuccess: () => {
      queryClient.invalidateQueries({ queryKey: ['merchant-requests'] });
      toast.success('Merchant request rejected');
      setShowReject(false);
      setRejectNote('');
    },
    onError: () => toast.error('Failed to reject request'),
  });

  const requests = data?.data?.requests ?? [];
  const total = data?.data?.total ?? 0;
  const totalPages = Math.ceil(total / 20);

  const columns: ColumnDef<MerchantRequest>[] = [
    { accessorKey: 'businessName', header: 'Business Name' },
    {
      accessorKey: 'businessType',
      header: 'Type',
      cell: ({ row }) => BUSINESS_TYPE_LABELS[row.original.businessType] || row.original.businessType,
    },
    {
      id: 'applicant',
      header: 'Applicant',
      cell: ({ row }) => (
        <div>
          <p className="font-medium">{row.original.user?.name}</p>
          <p className="text-xs text-slate-500">{row.original.user?.email}</p>
        </div>
      ),
    },
    {
      accessorKey: 'status',
      header: 'Status',
      cell: ({ row }) => (
        <StatusBadge
          status={row.original.status}
          colorMap={MERCHANT_REQUEST_STATUS_COLORS}
          labelMap={MERCHANT_REQUEST_STATUS_LABELS}
        />
      ),
    },
    {
      accessorKey: 'createdAt',
      header: 'Submitted',
      cell: ({ row }) => new Date(row.original.createdAt).toLocaleDateString(),
    },
    {
      id: 'actions',
      header: 'Actions',
      cell: ({ row }) => (
        <div className="flex gap-1">
          <Button size="sm" variant="ghost" onClick={() => { setSelected(row.original); setShowDetail(true); }}>
            <Eye className="h-4 w-4" />
          </Button>
          {canApprove && row.original.status === 'pending' && (
            <>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => { setSelected(row.original); setShowApprove(true); }}
              >
                <CheckCircle2 className="h-4 w-4 text-green-600" />
              </Button>
              <Button
                size="sm"
                variant="ghost"
                onClick={() => { setSelected(row.original); setShowReject(true); }}
              >
                <XCircle className="h-4 w-4 text-red-600" />
              </Button>
            </>
          )}
        </div>
      ),
    },
  ];

  return (
    <div>
      <PageHeader title="Merchant Requests" description="Review and manage merchant applications" />
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
      <DataTable columns={columns} data={requests} page={page} totalPages={totalPages} onPageChange={setPage} isLoading={isLoading} />

      {/* Detail Dialog */}
      <Dialog open={showDetail} onOpenChange={setShowDetail}>
        <DialogContent className="max-w-lg">
          <DialogHeader><DialogTitle>Merchant Application</DialogTitle></DialogHeader>
          {selected && (
            <div className="space-y-3 text-sm">
              <div className="grid grid-cols-2 gap-2">
                <div><span className="text-slate-500">Business Name:</span><p className="font-medium">{selected.businessName}</p></div>
                <div><span className="text-slate-500">Type:</span><p>{BUSINESS_TYPE_LABELS[selected.businessType]}</p></div>
                <div><span className="text-slate-500">Phone:</span><p>{selected.phone}</p></div>
                <div><span className="text-slate-500">Registration #:</span><p>{selected.registrationNumber || 'N/A'}</p></div>
                <div className="col-span-2"><span className="text-slate-500">Address:</span><p>{selected.address}, {selected.city}, {selected.state}, {selected.country}</p></div>
                {selected.businessDescription && (
                  <div className="col-span-2"><span className="text-slate-500">Description:</span><p>{selected.businessDescription}</p></div>
                )}
              </div>
              <div className="border-t pt-2">
                <span className="text-slate-500">Applicant:</span>
                <p className="font-medium">{selected.user?.name} ({selected.user?.email})</p>
                <p className="text-xs text-slate-400">Verification: {selected.user?.verificationStatus}</p>
              </div>
              {selected.adminNote && (
                <div className="border-t pt-2">
                  <span className="text-slate-500">Admin Note:</span>
                  <p>{selected.adminNote}</p>
                </div>
              )}
            </div>
          )}
        </DialogContent>
      </Dialog>

      {/* Approve Dialog */}
      <Dialog open={showApprove} onOpenChange={setShowApprove}>
        <DialogContent>
          <DialogHeader><DialogTitle>Approve Merchant Request</DialogTitle></DialogHeader>
          <p className="text-sm text-slate-600">Approve <strong>{selected?.businessName}</strong>? This will upgrade the user to merchant role.</p>
          <Textarea placeholder="Optional note..." value={approveNote} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setApproveNote(e.target.value)} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowApprove(false)}>Cancel</Button>
            <Button
              onClick={() => selected && approveMutation.mutate({ id: selected.id, note: approveNote || undefined })}
              disabled={approveMutation.isPending}
            >
              Approve
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>

      {/* Reject Dialog */}
      <Dialog open={showReject} onOpenChange={setShowReject}>
        <DialogContent>
          <DialogHeader><DialogTitle>Reject Merchant Request</DialogTitle></DialogHeader>
          <p className="text-sm text-slate-600">Reject <strong>{selected?.businessName}</strong>?</p>
          <Textarea placeholder="Reason for rejection (required)..." value={rejectNote} onChange={(e: React.ChangeEvent<HTMLTextAreaElement>) => setRejectNote(e.target.value)} />
          <DialogFooter>
            <Button variant="outline" onClick={() => setShowReject(false)}>Cancel</Button>
            <Button
              variant="destructive"
              onClick={() => selected && rejectMutation.mutate({ id: selected.id, note: rejectNote })}
              disabled={rejectMutation.isPending || !rejectNote.trim()}
            >
              Reject
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  );
}
