import { Alert } from 'react-native';
import { router } from 'expo-router';
import { useAuth } from '../AuthContext/AuthContext';

/**
 * Returns a gate function that checks email verification.
 * Call `requireVerified()` before any transactional action.
 * Returns true if verified, false if not (and shows a prompt).
 */
export function useEmailVerification() {
  const { emailVerified } = useAuth();

  const requireVerified = (): boolean => {
    if (emailVerified) return true;

    Alert.alert(
      'Email Verification Required',
      'Verify your email to unlock buying, selling, and wallet features.',
      [
        { text: 'Later', style: 'cancel' },
        {
          text: 'Verify Now',
          onPress: () => router.push('/verify'),
        },
      ]
    );
    return false;
  };

  return { emailVerified, requireVerified };
}
