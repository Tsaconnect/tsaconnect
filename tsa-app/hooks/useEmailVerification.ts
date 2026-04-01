import { Alert } from 'react-native';
import { router } from 'expo-router';
import { useAuth } from '../AuthContext/AuthContext';
import api from '../components/services/api';

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
          onPress: async () => {
            try {
              await api.sendOtp();
            } catch (_) {
              // OTP send failure is non-blocking; the verify screen has a resend button
            }
            router.push('/verify');
          },
        },
      ]
    );
    return false;
  };

  return { emailVerified, requireVerified };
}
