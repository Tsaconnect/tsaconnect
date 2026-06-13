// components/common/CartIconBadge.tsx
//
// Cart icon with an overlaid unread-count badge. Migrated onto the TSA Signature
// design system (theme tokens + @/components/ui primitives). The props interface,
// exports (including the re-exported useCartCount), the CartContext wiring, the
// navigation route (`/cart`), the default color/size props, and the 99+ count
// formatting are all preserved verbatim — this is a visual / structural restyle
// only.
import React from 'react';
import { View } from 'react-native';
import { useRouter } from 'expo-router';
import { useTheme } from '@/theme';
import { Icon, PressableScale, Text } from '@/components/ui';
import { useCartCount } from '@/contexts/CartContext';

// Re-exported for backward compatibility. The count now lives in CartContext so
// every badge shares one value and one (event-driven) refresh instead of each
// running its own poll. Requires <CartProvider> to be mounted (see app/_layout).
export { useCartCount };

interface CartIconBadgeProps {
  color?: string;
  size?: number;
}

export default function CartIconBadge({
  color = '#D4AF37',
  size = 24,
}: CartIconBadgeProps) {
  const router = useRouter();
  const { cartCount } = useCartCount();
  const { colors, radius } = useTheme();

  return (
    <PressableScale
      onPress={() => router.push('/cart')}
      style={{ padding: 6, position: 'relative' }}
    >
      <Icon name="cart" size={size} color={color} />
      {cartCount > 0 && (
        <View
          style={{
            position: 'absolute',
            top: 0,
            right: 0,
            backgroundColor: colors.danger,
            borderRadius: radius.pill,
            minWidth: 18,
            height: 18,
            justifyContent: 'center',
            alignItems: 'center',
            paddingHorizontal: 4,
            borderWidth: 1.5,
            borderColor: '#FFF',
          }}
        >
          <Text
            variant="overline"
            weight="700"
            style={{ color: '#FFF', fontSize: 10, lineHeight: 12, letterSpacing: 0 }}
          >
            {cartCount > 99 ? '99+' : cartCount}
          </Text>
        </View>
      )}
    </PressableScale>
  );
}
