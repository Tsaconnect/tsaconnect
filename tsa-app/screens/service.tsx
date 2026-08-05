// screens/service.tsx
//
// Services landing — the grid hub launching into the three money rails
// (Individual Payment, Payment Merchant, Instant Transfer), Trade & Earn, the
// marketplace, vendor browsing, listing flows, plus quick actions. Styled on the
// TSA Signature design system (theme tokens + @/components/ui primitives).
//
// Tile titles and routes are product copy — they come from constants/rails.ts
// so the rail names stay consistent with the wallet and marketplace.
import { router } from 'expo-router';
import React from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Card,
  Icon,
  IconName,
  PressableScale,
  Text,
} from '@/components/ui';
import { RAILS, RAIL_ROUTES, railLabel } from '@/constants/rails';

// Each tile carries a route + the accent hue used to tint its icon chip. Tokens
// drive the surfaces/typography; the per-item `tone` keys into the theme so the
// catalog stays theme-adaptive (no hardcoded hex).
type Tone = 'accent' | 'success' | 'info' | 'escrow' | 'warning';

interface ServiceItem {
  key: string;
  title: string;
  description: string;
  icon: IconName;
  tone: Tone;
  route: string;
}

interface QuickAction {
  key: string;
  label: string;
  icon: IconName;
  tone: Tone;
  route: string;
}

// The three money rails plus Trade & Earn lead the grid — Services is the menu
// users are told to find them in, so they sit above the listing/browse tiles.
const SERVICE_ITEMS: ServiceItem[] = [
  {
    key: 'p2p',
    title: railLabel('p2p'),
    description: RAILS.p2p.tagline,
    icon: 'user',
    tone: 'warning',
    route: RAIL_ROUTES.p2pBuy,
  },
  {
    key: 'pm',
    title: railLabel('pm'),
    description: RAILS.pm.tagline,
    icon: 'storefront',
    tone: 'info',
    route: RAIL_ROUTES.pm,
  },
  {
    key: 'it',
    title: railLabel('it'),
    description: RAILS.it.tagline,
    icon: 'bolt',
    tone: 'accent',
    route: RAIL_ROUTES.it,
  },
  {
    key: 'pay-bills',
    title: 'Pay Bills',
    description: 'Airtime, data, electricity & more — paid with MCGP',
    icon: 'bolt',
    tone: 'warning',
    route: '/bills',
  },
  {
    key: 'trade-earn',
    title: 'Trade & Earn',
    description: 'Trade Points, cashback and referral rewards',
    icon: 'trendingUp',
    tone: 'success',
    route: '/trade',
  },
  {
    key: 'marketplace',
    title: 'Marketplace',
    description: 'Browse products by category',
    icon: 'storefront',
    tone: 'success',
    route: '/products',
  },
  {
    key: 'browse-services',
    title: 'Browse Services',
    description: 'Find vendors by category',
    icon: 'search',
    tone: 'info',
    route: '/service',
  },
  {
    key: 'register-product',
    title: 'List Product',
    description: 'Register a product to sell',
    icon: 'plus',
    tone: 'escrow',
    route: '/serviceaction?index=0',
  },
  {
    key: 'register-service',
    title: 'List Service',
    description: 'Register a service to offer',
    icon: 'upload',
    tone: 'accent',
    route: '/serviceaction?index=1',
  },
];

const QUICK_ACTIONS: QuickAction[] = [
  // Opens the wallet with its Deposit sheet already up — the sheet is the one
  // place that lists every deposit rail, so this shortcut shouldn't preempt it
  // by dropping the user into a single one.
  { key: 'deposit', label: 'Deposit', icon: 'wallet', tone: 'success', route: '/tokenization?sheet=deposit' },
  { key: 'my-listings', label: 'My Listings', icon: 'list', tone: 'info', route: '/myAdverts' },
  { key: 'merchant', label: 'Become Merchant', icon: 'storefront', tone: 'accent', route: '/merchants/merchant-request' },
  { key: 'wallet', label: 'Wallet', icon: 'bank', tone: 'escrow', route: '/tokenization' },
];

const ServicesScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();

  // Maps a tile's logical tone to its (foreground, tinted-background) theme pair.
  const toneColors = (tone: Tone): { fg: string; bg: string } => {
    switch (tone) {
      case 'success':
        return { fg: colors.success, bg: colors.successBg };
      case 'info':
        return { fg: colors.info, bg: colors.infoBg };
      case 'escrow':
        return { fg: colors.escrow, bg: colors.escrowBg };
      case 'warning':
        return { fg: colors.warning, bg: colors.warningBg };
      case 'accent':
      default:
        return { fg: colors.accent, bg: colors.accentMuted };
    }
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        showsVerticalScrollIndicator={false}
      >
        {/* Header */}
        <Text variant="title1" weight="700">
          Services
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
          Explore what TSA Connect offers
        </Text>

        {/* Service Grid */}
        <View
          style={{
            flexDirection: 'row',
            flexWrap: 'wrap',
            gap: spacing.md,
            marginTop: spacing.xl,
          }}
        >
          {SERVICE_ITEMS.map((item) => {
            const tone = toneColors(item.tone);
            return (
              <PressableScale
                key={item.key}
                // Route strings are dynamic across the catalog; expo-router's typed
                // routes can't narrow them, so cast at the navigation boundary.
                onPress={() => router.push(item.route as never)}
                style={{ flexGrow: 1, flexBasis: '47%' }}
              >
                {/* No height:'100%' — a percentage height resolves against the
                    ScrollView's unbounded measuring height and blows the card up
                    to full-screen. The row's default alignItems:'stretch' already
                    equalizes the two cells; tiles size to their (uniform) content. */}
                <Card padding="lg">
                  <View
                    style={{
                      width: 48,
                      height: 48,
                      borderRadius: radius.md,
                      alignItems: 'center',
                      justifyContent: 'center',
                      backgroundColor: tone.bg,
                      marginBottom: spacing.md,
                    }}
                  >
                    <Icon name={item.icon} size={26} color={tone.fg} />
                  </View>
                  {/* Two lines: the expanded rail names ("Individual Payment
                      (P2P)") don't fit a half-width tile on one. */}
                  <Text variant="bodyMed" weight="700" numberOfLines={2}>
                    {item.title}
                  </Text>
                  <Text
                    variant="caption"
                    color="textMuted"
                    numberOfLines={2}
                    style={{ marginTop: spacing.xs }}
                  >
                    {item.description}
                  </Text>
                </Card>
              </PressableScale>
            );
          })}
        </View>

        {/* Quick Actions */}
        <Text variant="title3" weight="700" style={{ marginTop: spacing['2xl'], marginBottom: spacing.md }}>
          Quick Actions
        </Text>
        <View
          style={{
            flexDirection: 'row',
            flexWrap: 'wrap',
            gap: spacing.md,
          }}
        >
          {QUICK_ACTIONS.map((item) => {
            const tone = toneColors(item.tone);
            return (
              <PressableScale
                key={item.key}
                onPress={() => router.push(item.route as never)}
                style={{ flexGrow: 1, flexBasis: '47%' }}
              >
                <Card padding="lg" style={{ alignItems: 'center' }}>
                  <View
                    style={{
                      width: 52,
                      height: 52,
                      borderRadius: radius.pill,
                      alignItems: 'center',
                      justifyContent: 'center',
                      backgroundColor: tone.bg,
                      marginBottom: spacing.sm,
                    }}
                  >
                    <Icon name={item.icon} size={26} color={tone.fg} />
                  </View>
                  <Text variant="bodySm" weight="600" numberOfLines={1}>
                    {item.label}
                  </Text>
                </Card>
              </PressableScale>
            );
          })}
        </View>

        {/* P2P Highlight Banner */}
        <PressableScale onPress={() => router.push(RAIL_ROUTES.p2pBuy as never)} style={{ marginTop: spacing['2xl'] }}>
          <AccentCard padding="lg">
            <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
              <View
                style={{
                  width: 48,
                  height: 48,
                  borderRadius: radius.pill,
                  alignItems: 'center',
                  justifyContent: 'center',
                  backgroundColor: colors.accentMuted,
                }}
              >
                <Icon name="swap" size={28} color={colors.accent} />
              </View>
              <View style={{ flex: 1 }}>
                <Text variant="bodyMed" weight="700">
                  TSA Connect {RAILS.p2p.name}
                </Text>
                <Text variant="caption" color="textSecondary" style={{ marginTop: 2 }}>
                  Browse open ads or post your own to trade crypto with verified payment proofs
                </Text>
              </View>
              <Icon name="chevronRight" size={22} color={colors.accent} />
            </View>
          </AccentCard>
        </PressableScale>
      </ScrollView>
    </SafeAreaView>
  );
};

export default ServicesScreen;
