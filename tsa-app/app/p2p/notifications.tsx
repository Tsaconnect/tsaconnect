// app/p2p/notifications.tsx
//
// Notifications inbox reachable from the P2P section (via the header bell), so
// tapping a trade notification deep-links back into the P2P stack with clean
// back behavior. Renders the shared NotificationInbox.
import React from 'react';
import NotificationInbox from '@/components/notifications/NotificationInbox';

const P2PNotificationsScreen: React.FC = () => <NotificationInbox />;

export default P2PNotificationsScreen;
