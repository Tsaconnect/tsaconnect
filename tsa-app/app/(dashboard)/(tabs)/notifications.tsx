// app/(dashboard)/(tabs)/notifications.tsx
//
// Dashboard notifications tab. Thin wrapper around the shared NotificationInbox
// (single source of truth — also used by the P2P-stack route).
import React from 'react';
import NotificationInbox from '@/components/notifications/NotificationInbox';

const NotificationsScreen: React.FC = () => <NotificationInbox />;

export default NotificationsScreen;
