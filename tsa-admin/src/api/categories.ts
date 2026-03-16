import client from './client';
import type { ApiResponse, Category } from '@/types';

export async function getCategories(params?: { type?: string; active?: boolean }) {
  const { data } = await client.get<ApiResponse<Category[]>>('/products/category/all', { params });
  return data;
}

export async function getCategoryTree() {
  const { data } = await client.get<ApiResponse<Category[]>>('/products/category/tree');
  return data;
}

export async function createCategory(categoryData: FormData) {
  const { data } = await client.post<ApiResponse<Category>>('/products/category', categoryData, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return data;
}

export async function updateCategory(id: string, categoryData: FormData) {
  const { data } = await client.put<ApiResponse<Category>>(`/products/category/${id}`, categoryData, {
    headers: { 'Content-Type': 'multipart/form-data' },
  });
  return data;
}

export async function deleteCategory(id: string) {
  const { data } = await client.delete<ApiResponse>(`/products/category/${id}`);
  return data;
}

export async function reorderCategories(orderedIds: string[]) {
  const orders = orderedIds.map((id, index) => ({ id, order: index }));
  const { data } = await client.patch<ApiResponse>('/products/category/reorder', { orders });
  return data;
}
