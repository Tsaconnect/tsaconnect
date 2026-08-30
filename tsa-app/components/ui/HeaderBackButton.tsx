// components/ui/HeaderBackButton.tsx
//
// The chevron every native Stack header uses, wired to the shared back action.
//
// Why not just let native-stack draw its own? Because it only draws one when the
// LOCAL navigator can pop. A screen reached from another group — /wallet/send
// from the wallet tab, /bills from the services hub, /servicedetail from a
// seller page — is route 0 of a freshly created nested stack, so native-stack
// renders nothing, even though the root stack can unwind perfectly well. That
// gap was the single biggest source of "this page has no back button".
//
// Supplying headerLeft for every screen also settles the second half of the
// problem: one glyph, one size, one tint, whether the header is native chrome or
// a hand-rolled row (ScreenHeader renders the same IconButton).
import React from 'react';
import { type Href } from 'expo-router';
import { useTheme } from '@/theme';
import { IconButton } from './IconButton';
import { goBack } from '@/utils/back';

export interface HeaderBackButtonProps {
  /** Where to land when there is no history at all (cold deep link). */
  fallback: Href;
}

export const HeaderBackButton: React.FC<HeaderBackButtonProps> = ({ fallback }) => {
  const { colors, spacing } = useTheme();

  return (
    <IconButton
      icon="chevronLeft"
      color={colors.accent}
      accessibilityLabel="Go back"
      onPress={() => goBack(fallback)}
      // Native headers already inset their left slot; cancel the IconButton's
      // own 44pt padding so the chevron sits where the native one would.
      style={{ marginLeft: -spacing.sm }}
    />
  );
};

/**
 * `headerLeft` factory for a Stack's screenOptions.
 *
 * Always renders. The chevron is never dead: with history it pops, and without
 * it (a cold deep link, a notification tap) it replaces with the section
 * fallback — which is the only thing standing between the user and a screen
 * they cannot leave, since a group root shows neither the tab bar nor a drawer.
 */
export const headerBackLeft =
  (fallback: Href) =>
  // eslint-disable-next-line react/display-name
  () => <HeaderBackButton fallback={fallback} />;
