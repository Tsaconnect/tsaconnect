// Schedules a local notification ~5 min before a pending_payment trade's expiry,
// and cancels it when the trade leaves pending_payment (or the screen unmounts).
// App-side only (works offline); native, so it no-ops in Expo Go / simulators.
import { useEffect } from 'react';
import * as Device from 'expo-device';
import * as Notifications from 'expo-notifications';
import type { P2PTrade } from '../services/p2p';

const LEAD_MS = 5 * 60 * 1000; // fire ~5 min before expiry

export function useTradeExpiryReminder(trade: P2PTrade | null): void {
  useEffect(() => {
    if (!Device.isDevice) return;
    if (!trade || trade.status !== 'pending_payment' || !trade.expiresAt) return;
    const fireInMs = new Date(trade.expiresAt).getTime() - LEAD_MS - Date.now();
    if (fireInMs <= 0) return; // not enough runway for a useful reminder

    let id: string | null = null;
    let cancelled = false;
    (async () => {
      try {
        const { status } = await Notifications.getPermissionsAsync();
        if (status !== 'granted' || cancelled) return;
        id = await Notifications.scheduleNotificationAsync({
          content: {
            title: 'Payment window closing',
            body: `~5 minutes left to pay for your ${trade.token} trade.`,
            data: { type: 'p2p.trade.expiry_reminder', tradeId: trade.id },
          },
          trigger: {
            type: Notifications.SchedulableTriggerInputTypes.TIME_INTERVAL,
            seconds: Math.round(fireInMs / 1000),
          },
        });
      } catch (e) {
        console.warn('[push] expiry reminder skipped:', (e as Error)?.message ?? e);
      }
    })();

    return () => {
      cancelled = true;
      if (id) Notifications.cancelScheduledNotificationAsync(id).catch(() => {});
    };
    // Keyed on the specific fields that matter, not the whole `trade` object, so
    // a re-fetch with identical values doesn't reschedule.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [trade?.id, trade?.status, trade?.expiresAt, trade?.token]);
}
