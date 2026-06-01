import React, { createContext, useContext, useEffect, useRef, useState, useCallback } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { API_BASE_URL } from '../constants/api/config';

export interface Notification {
  id: string;
  type: string;
  title: string;
  message: string;
  data: Record<string, any>;
  isRead: boolean;
  createdAt: string;
}

/** Inbound WS frame listener. Fired for every notification the socket delivers,
 *  so screens (e.g. the P2P trade cockpit) can react to a relevant event without
 *  their own poll. Returns an unsubscribe. */
export type NotificationListener = (n: Notification) => void;

interface NotificationContextType {
  unreadCount: number;
  notifications: Notification[];
  setNotifications: React.Dispatch<React.SetStateAction<Notification[]>>;
  refreshUnreadCount: () => Promise<void>;
  connected: boolean;
  subscribe: (listener: NotificationListener) => () => void;
}

const NotificationContext = createContext<NotificationContextType>({
  unreadCount: 0,
  notifications: [],
  setNotifications: () => {},
  refreshUnreadCount: async () => {},
  connected: false,
  subscribe: () => () => {},
});

export const useNotifications = () => useContext(NotificationContext);

export const NotificationProvider: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const [unreadCount, setUnreadCount] = useState(0);
  const [notifications, setNotifications] = useState<Notification[]>([]);
  const [connected, setConnected] = useState(false);
  const wsRef = useRef<WebSocket | null>(null);
  const reconnectTimeout = useRef<ReturnType<typeof setTimeout>>();
  const reconnectDelay = useRef(1000);
  // Raw-frame listeners (e.g. the trade cockpit). Kept in a ref so the WS
  // callbacks always see the current set without re-binding the socket.
  const listenersRef = useRef<Set<NotificationListener>>(new Set());

  const subscribe = useCallback((listener: NotificationListener) => {
    listenersRef.current.add(listener);
    return () => {
      listenersRef.current.delete(listener);
    };
  }, []);

  const wsUrl = API_BASE_URL.replace(/^http/, 'ws').replace(/\/api$/, '');

  const refreshUnreadCount = useCallback(async () => {
    try {
      const token = await AsyncStorage.getItem('token');
      if (!token) return;
      const res = await fetch(`${API_BASE_URL}/notifications/unread-count`, {
        headers: { Authorization: `Bearer ${token}` },
      });
      const json = await res.json();
      if (json.success) {
        setUnreadCount(json.data.count);
      }
    } catch {}
  }, []);

  const connect = useCallback(async () => {
    const token = await AsyncStorage.getItem('token');
    if (!token) return;

    if (wsRef.current?.readyState === WebSocket.OPEN) return;

    const ws = new WebSocket(`${wsUrl}/ws/notifications?token=${token}`);

    ws.onopen = () => {
      setConnected(true);
      reconnectDelay.current = 1000;
    };

    ws.onmessage = (event) => {
      try {
        const notification: Notification = JSON.parse(event.data);
        setNotifications((prev) => [notification, ...prev]);
        setUnreadCount((prev) => prev + 1);
        // Fan out to raw-frame subscribers (cockpit, etc.). Guard each so one
        // throwing listener can't break notification delivery to the rest.
        listenersRef.current.forEach((listener) => {
          try {
            listener(notification);
          } catch {}
        });
      } catch {}
    };

    ws.onclose = () => {
      setConnected(false);
      reconnectTimeout.current = setTimeout(() => {
        reconnectDelay.current = Math.min(reconnectDelay.current * 2, 30000);
        connect();
      }, reconnectDelay.current);
    };

    ws.onerror = () => {
      ws.close();
    };

    wsRef.current = ws;
  }, [wsUrl]);

  const disconnect = useCallback(() => {
    if (reconnectTimeout.current) clearTimeout(reconnectTimeout.current);
    if (wsRef.current) {
      wsRef.current.onclose = null;
      wsRef.current.close();
      wsRef.current = null;
    }
    setConnected(false);
  }, []);

  // Defer notification setup — don't block app startup
  useEffect(() => {
    const timer = setTimeout(() => {
      connect();
      refreshUnreadCount();
    }, 3000);
    return () => {
      clearTimeout(timer);
      disconnect();
    };
  }, [connect, disconnect, refreshUnreadCount]);

  return (
    <NotificationContext.Provider
      value={{ unreadCount, notifications, setNotifications, refreshUnreadCount, connected, subscribe }}
    >
      {children}
    </NotificationContext.Provider>
  );
};
