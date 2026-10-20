// The referral share message, shared by the Referrals screen and the promotion unlock card.
import { Share } from 'react-native';

export function referralCodeOf(profile: { referralCode?: string; username?: string } | null | undefined) {
  return profile?.referralCode || profile?.username || '';
}

export async function shareReferralCode(code: string): Promise<void> {
  await Share.share({
    message: `Join TSA Connect using my referral code: ${code}`,
    title: 'TSA Connect Referral',
  });
}
