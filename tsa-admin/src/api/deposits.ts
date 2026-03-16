import client from './client';
import type { ApiResponse, Deposit, PaginationMeta } from '@/types';

export async function getDeposits(params: { page?: number; limit?: number; status?: string }) {
  const { data } = await client.get<ApiResponse<{ deposits: Deposit[]; pagination: PaginationMeta }>>('/deposits', { params });
  return data;
}

export async function updateDepositStatus(id: string, status: 'approved' | 'rejected', note?: string) {
  const { data } = await client.patch<ApiResponse>(`/deposits/${id}/status`, { status, note });
  return data;
}
