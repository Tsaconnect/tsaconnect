// Customer–provider conversations (API: /chat/*, /services/:id/chat).
// Reuses the P2P client's request helper: same auth, envelope and errors.
import { request, type ApiResponse } from './p2p';

export interface ChatThread {
  id: string;
  subjectType: string;
  subjectId: string;
  customerId: string;
  providerId: string;
  lastMessageAt: string | null;
  lastMessagePreview: string;
  lastSenderId?: string;
  createdAt: string;
  /** The caller's side of the conversation. */
  role: 'customer' | 'provider';
  counterpartId: string;
  counterpartName: string;
  subjectTitle: string;
  subjectImage?: string;
  unread: boolean;
}

export interface ChatMessage {
  id: string;
  threadId: string;
  senderId: string;
  type: 'text' | 'image';
  body?: string;
  imageUrl?: string;
  createdAt: string;
}

export const chatApi = {
  /** The caller's conversation with a contact service's provider (fee paid). */
  openForService: (serviceId: string): Promise<ApiResponse<ChatThread>> =>
    request<ChatThread>('POST', `/services/${serviceId}/chat`, undefined, { idempotent: false }),
  threads: (): Promise<ApiResponse<ChatThread[]>> => request<ChatThread[]>('GET', '/chat/threads'),
  thread: (threadId: string): Promise<ApiResponse<ChatThread>> =>
    request<ChatThread>('GET', `/chat/threads/${threadId}`),
  unreadCount: (): Promise<ApiResponse<{ count: number }>> =>
    request<{ count: number }>('GET', '/chat/unread-count'),
  messages: (threadId: string, since?: string): Promise<ApiResponse<ChatMessage[]>> =>
    request<ChatMessage[]>('GET', `/chat/threads/${threadId}/messages`, undefined, { query: { since } }),
  sendText: (threadId: string, body: string): Promise<ApiResponse<ChatMessage>> =>
    request<ChatMessage>('POST', `/chat/threads/${threadId}/messages`, { type: 'text', body }, { idempotent: false }),
  sendImage: (threadId: string, imageUrl: string): Promise<ApiResponse<ChatMessage>> =>
    request<ChatMessage>('POST', `/chat/threads/${threadId}/messages`, { type: 'image', imageUrl }, { idempotent: false }),
  markRead: (threadId: string): Promise<ApiResponse<null>> =>
    request<null>('POST', `/chat/threads/${threadId}/read`, undefined, { idempotent: false }),
};
