import client from './client';
import type { ApiResponse, Order, PaginationMeta } from '@/types';

export async function getOrders(params: { page?: number; limit?: number; status?: string }) {
  const { data } = await client.get<ApiResponse<{ orders: Order[]; pagination: PaginationMeta }>>('/orders', { params });
  return data;
}

export async function getOrderById(id: string) {
  const { data } = await client.get<ApiResponse<Order>>(`/orders/${id}`);
  return data;
}

export async function updateOrderStatus(id: string, status: string, notes?: string) {
  const { data } = await client.patch<ApiResponse>(`/orders/${id}/status`, { status, notes });
  return data;
}
