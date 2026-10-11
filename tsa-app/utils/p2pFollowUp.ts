// utils/p2pFollowUp.ts — the one place a P2P screen turns a follow-up
// (services/p2pTx.P2PFollowUp) into UI: the KYC prompt, a toast, an alert,
// navigation and the reload. Screens build the follow-up with p2pTxFollowUp
// (or a helper over it) and hand it here, so every P2P action routes a KYC
// refusal, offers "View trade" and reloads the same way.
import { Alert, type AlertButton } from 'react-native';
import type { P2PFollowUp } from '@/services/p2pTx';
import { promptKycRefusal } from '@/utils/kycRefusal';

export interface FollowUpHandlers {
  toast?: { show: (message: string, type?: 'success' | 'error' | 'info') => void };
  reload?: () => void;
  /** Go to a trade (P2PFollowUp.navigate or the alert's "View trade"). */
  navigate?: (tradeId: string) => void;
}

export function applyFollowUp(next: P2PFollowUp, h: FollowUpHandlers): void {
  if (next.kyc && promptKycRefusal(next.kyc.code, next.kyc.message, 'p2p')) return;
  if (next.toast) h.toast?.show(next.toast.message, next.toast.type);
  const { alert, navigate } = next;
  if (alert) {
    let buttons: AlertButton[] | undefined;
    const viewTradeId = alert.viewTradeId;
    if (viewTradeId) {
      buttons = [
        { text: alert.dismissText ?? 'OK', style: 'cancel' },
        { text: 'View trade', onPress: () => h.navigate?.(viewTradeId) },
      ];
    } else if (navigate) {
      buttons = [{ text: 'OK', onPress: () => h.navigate?.(navigate) }];
    }
    Alert.alert(alert.title, alert.message, buttons);
  } else if (navigate) {
    h.navigate?.(navigate);
  }
  if (next.reload) h.reload?.();
}
