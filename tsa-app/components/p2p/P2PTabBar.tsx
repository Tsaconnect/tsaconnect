// P2PTabBar — the persistent bottom navigation footer for the P2P section
// (Bybit-style). Rendered once in app/p2p/_layout.tsx so it stays mounted across
// P2P screens; it auto-hides on focused detail/flow screens (ad detail, trade
// cockpit, create/forms) where a bottom nav would get in the way.
//
// Uses Ionicons directly (not the design-system Icon registry) since a nav bar's
// glyphs are self-contained — this keeps it independent of in-content iconography.
import React from 'react';
import { Pressable, View } from 'react-native';
import { router, usePathname } from 'expo-router';
import { Ionicons } from '@expo/vector-icons';
import { useSafeAreaInsets } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';

type Glyph = React.ComponentProps<typeof Ionicons>['name'];
interface Tab {
  label: string;
  route: string;
  icon: Glyph;
  iconActive: Glyph;
}

// Tabs map to the existing P2P landing screens. (Creating an ad is a CTA on the
// Market screen, not a tab.) The footer shows ONLY on these routes.
const TABS: Tab[] = [
  { label: 'Market', route: '/p2p', icon: 'storefront-outline', iconActive: 'storefront' },
  { label: 'Orders', route: '/p2p/my-trades', icon: 'receipt-outline', iconActive: 'receipt' },
  { label: 'Ads', route: '/p2p/my-ads', icon: 'pricetags-outline', iconActive: 'pricetags' },
  { label: 'Banks', route: '/p2p/payment-methods', icon: 'card-outline', iconActive: 'card' },
  { label: 'Pool', route: '/p2p/pool', icon: 'wallet-outline', iconActive: 'wallet' },
];

function normalize(p: string): string {
  return p.length > 1 && p.endsWith('/') ? p.slice(0, -1) : p;
}

export const P2PTabBar: React.FC = () => {
  const { colors, spacing } = useTheme();
  const insets = useSafeAreaInsets();
  const current = normalize(usePathname() || '');

  // Only render on the landing screens; hidden on detail/flow screens.
  if (!TABS.some((t) => t.route === current)) return null;

  return (
    <View
      style={{
        flexDirection: 'row',
        backgroundColor: colors.surface,
        borderTopWidth: 1,
        borderTopColor: colors.hairline,
        paddingTop: spacing.sm,
        paddingBottom: Math.max(insets.bottom, spacing.sm),
      }}
    >
      {TABS.map((t) => {
        const active = t.route === current;
        const tint = active ? colors.accent : colors.textMuted;
        return (
          <Pressable
            key={t.route}
            onPress={() => {
              if (!active) router.navigate(t.route as never);
            }}
            accessibilityRole="tab"
            accessibilityState={{ selected: active }}
            accessibilityLabel={t.label}
            style={{ flex: 1, alignItems: 'center', justifyContent: 'center', gap: 3, minHeight: 44 }}
          >
            <Ionicons name={active ? t.iconActive : t.icon} size={22} color={tint} />
            <Text variant="overline" weight={active ? '700' : '600'} style={{ color: tint }}>
              {t.label}
            </Text>
          </Pressable>
        );
      })}
    </View>
  );
};
