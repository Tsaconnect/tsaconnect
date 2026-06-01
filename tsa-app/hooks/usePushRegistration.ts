// Expo push registration + tap-to-navigate. Native (expo-notifications/-device),
// so it no-ops safely in Expo Go / on simulators (guarded by Device.isDevice +
// try/catch) — real delivery needs a dev/EAS build.
//
// On login: request permission → get the Expo push token → register it with the
// backend (upsert). On a notification tap: deep-link via routeForNotification.
import { useEffect } from 'react';
import { Platform } from 'react-native';
import * as Device from 'expo-device';
import * as Notifications from 'expo-notifications';
import Constants from 'expo-constants';
import { router } from 'expo-router';
import { useAuth } from '../AuthContext/AuthContext';
import { registerDevice } from '../services/notifications';
import { routeForNotification, type NotificationData } from './notificationRouting';

// Foreground behavior: show the banner + list and play a sound even with the app
// open (don't touch the app icon badge).
Notifications.setNotificationHandler({
  handleNotification: async () => ({
    shouldShowBanner: true,
    shouldShowList: true,
    shouldPlaySound: true,
    shouldSetBadge: false,
  }),
});

async function registerForPush(): Promise<void> {
  if (!Device.isDevice) return; // no push tokens on simulators / Expo Go web
  const existing = await Notifications.getPermissionsAsync();
  let status = existing.status;
  if (status !== 'granted') {
    status = (await Notifications.requestPermissionsAsync()).status;
  }
  if (status !== 'granted') return;

  if (Platform.OS === 'android') {
    await Notifications.setNotificationChannelAsync('default', {
      name: 'Default',
      importance: Notifications.AndroidImportance.HIGH,
    });
  }

  const projectId =
    Constants.expoConfig?.extra?.eas?.projectId ?? (Constants as any).easConfig?.projectId;
  const tokenResp = await Notifications.getExpoPushTokenAsync(
    projectId ? { projectId } : undefined,
  );
  await registerDevice(tokenResp.data, Platform.OS);
}

export function usePushRegistration(): void {
  const { isAuthenticated } = useAuth();

  // Register once the user is authenticated. Re-running on a re-login re-points
  // the token to the current user (server upserts by token).
  useEffect(() => {
    if (!isAuthenticated) return;
    let cancelled = false;
    registerForPush().catch((e) => {
      if (!cancelled) console.warn('[push] registration skipped:', e?.message ?? e);
    });
    return () => {
      cancelled = true;
    };
  }, [isAuthenticated]);

  // Deep-link on tap — independent of auth so a tap that cold-starts the app routes too.
  useEffect(() => {
    const sub = Notifications.addNotificationResponseReceivedListener((response) => {
      const data = response.notification.request.content.data as NotificationData;
      const route = routeForNotification(data);
      if (route) router.push(route as never);
    });
    return () => sub.remove();
  }, []);
}
