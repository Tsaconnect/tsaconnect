// constants/navigation.ts
//
// The ONE header definition for every Stack in the app.
//
// Before this, nine layouts each declared their own `screenOptions` object with
// the same six properties copy-pasted, and `app/adverts` declared none at all
// (so it rendered React Navigation's unstyled default header on top of a screen
// that already drew its own). They are all sourced from here now, which is also
// what makes the back chevron uniform: `headerLeft` is part of the shared
// options, so a group cannot accidentally ship without one.
//
// ── Why headerLeft is overridden at all ──────────────────────────────────────
// expo-router builds a nested navigator's state from the URL. Without an anchor
// (`unstable_settings.initialRouteName`) that state is a SINGLE route — see
// expo-router/build/fork/getStateFromPath.js `createStateObject` — so any screen
// entered from another group sits at index 0 and native-stack draws no back
// button, even though `router.back()` would correctly unwind through the root
// stack to the caller. Supplying our own `headerLeft` closes that gap without
// fabricating history the user never visited (which is what an anchor does).
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import type { Href } from 'expo-router';
import { useTheme, fontFamily } from '@/theme';
import { headerBackLeft } from '@/components/ui/HeaderBackButton';
import { HOME_TAB } from '@/constants/routes';

export interface ScreenOptionsConfig {
  /**
   * Where this section's screens land when back is pressed with no history —
   * a cold start from a deep link, a push notification, or an OAuth callback.
   *
   * Name the SECTION (the wallet tab for wallet screens, the services tab for
   * bills), not the app root. Defaulting everything to Home is what produced
   * the "back takes me to main" reports.
   */
  backFallback?: Href;
}

export function useDefaultScreenOptions(
  config: ScreenOptionsConfig = {},
): NativeStackNavigationOptions {
  const { colors, type } = useTheme();
  const { backFallback = HOME_TAB } = config;

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
    headerLeft: headerBackLeft(backFallback),
  };
}
