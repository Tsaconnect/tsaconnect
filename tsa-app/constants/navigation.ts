// Shared navigation header configuration
// Used across all Stack layouts for consistent header styling.
//
// Prefer the `useDefaultScreenOptions()` hook — it sources header background,
// tint, title color and font from useTheme() tokens so the header tracks light
// & dark automatically (mirrors the wallet/p2p/(productgroup) layouts). The
// static `defaultScreenOptions` constant below is the legacy hardcoded-white
// fallback, kept only for non-hook callers.
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';

export function useDefaultScreenOptions(): NativeStackNavigationOptions {
  const { colors, type } = useTheme();

  return {
    headerTitleAlign: 'center',
    headerTintColor: colors.accent,
    headerTitleStyle: {
      fontSize: type.title3.fontSize,
      fontFamily: fontFamily['600'],
      color: colors.textPrimary,
    },
    headerStyle: {
      backgroundColor: colors.surface,
    },
    contentStyle: {
      backgroundColor: colors.background,
    },
    headerShadowVisible: false,
  };
}

const HEADER_TINT = '#9D6B38';

export const defaultScreenOptions: NativeStackNavigationOptions = {
  headerTitleAlign: 'center',
  headerTintColor: HEADER_TINT,
  headerTitleStyle: {
    fontSize: 17,
    fontWeight: '600',
    color: '#1f2937',
  },
  headerStyle: {
    backgroundColor: '#FFFFFF',
  },
  headerShadowVisible: false,
};
