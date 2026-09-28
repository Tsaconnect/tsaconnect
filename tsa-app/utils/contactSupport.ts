// utils/contactSupport.ts
import { Alert, Linking } from 'react-native';
import { COMPANY } from '@/constants/legal';

/** Opens an email to support; where no mail app can take it, shows the address instead. */
export function emailSupport(subject?: string): void {
  const query = subject ? `?subject=${encodeURIComponent(subject)}` : '';
  Linking.openURL(`mailto:${COMPANY.supportEmail}${query}`).catch(() =>
    Alert.alert('Support', `Email us at ${COMPANY.supportEmail}`),
  );
}
