import client from './client';
import type { ApiResponse, MerchantRequest } from '@/types';

export async function getMerchantRequests(params: { page?: number; limit?: number; status?: string }) {
  const { data } = await client.get<ApiResponse<{ requests: MerchantRequest[]; total: number; page: number; limit: number }>>(
    '/admin/merchant-requests',
    { params }
  );
  return data;
}

export async function approveMerchantRequest(id: string, note?: string) {
  const { data } = await client.post<ApiResponse<MerchantRequest>>(
    `/admin/merchant-requests/${id}/approve`,
    { note }
  );
  return data;
}

export async function rejectMerchantRequest(id: string, note: string) {
  const { data } = await client.post<ApiResponse<MerchantRequest>>(
    `/admin/merchant-requests/${id}/reject`,
    { note }
  );
  return data;
}
