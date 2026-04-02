import { Alert } from 'react-native';
import { router } from 'expo-router';
import { useAuth } from '../AuthContext/AuthContext';

/**
 * Returns a gate function that checks KYC verification status.
 * Call `requireKycVerified()` before checkout or wallet transactions.
 * Returns true if verified, false if not (and shows a prompt).
 */
export function useKycVerification() {
  const { currentUser } = useAuth();
  const isKycVerified = currentUser?.verificationStatus === 'verified';

  const requireKycVerified = (): boolean => {
    if (isKycVerified) return true;

    const status = currentUser?.verificationStatus;

    if (status === 'in_review') {
      Alert.alert(
        'Verification In Progress',
        'Your identity verification is being reviewed. You\'ll be able to transact once approved.',
      );
    } else {
      Alert.alert(
        'Identity Verification Required',
        'Please complete identity verification to unlock buying, selling, and wallet features.',
        [
          { text: 'Later', style: 'cancel' },
          {
            text: 'Verify Now',
            onPress: () => router.push('/profile'),
          },
        ]
      );
    }
    return false;
  };

  return { isKycVerified, requireKycVerified };
}
