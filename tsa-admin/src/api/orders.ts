import client from './client';
import type { ApiResponse, Order, PaginationMeta } from '@/types';

export async function getAdminOrders(params: { page?: number; limit?: number; status?: string; search?: string }) {
  const { data } = await client.get<ApiResponse<{ orders: Order[]; pagination: PaginationMeta }>>('/admin/orders', { params });
  return data;
}

export async function getOrderById(id: string) {
  const { data } = await client.get<ApiResponse<Order>>(`/orders/${id}`);
  return data;
}

export async function resolveDispute(id: string, refundBuyer: boolean) {
  const { data } = await client.post<ApiResponse>(`/admin/orders/${id}/resolve`, { refundBuyer });
  return data;
}
