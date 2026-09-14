// Token logo for the Instant Transfer screens. MCGP ships with the app; the
// stablecoins use the logos already registered in DEFAULT_TOKENS. A logo that
// fails to load falls back to a lettered circle in the token's brand colour,
// never a blank disc.
import React, { useState } from 'react';
import { Image, View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';
import { DEFAULT_TOKENS } from '@/constants/chains';

const MCGP_LOGO = require('../../assets/digital/mcgp.png');

export const TOKEN_AVATAR_SIZE = 40;

export default function ITTokenAvatar({
  token,
  size = TOKEN_AVATAR_SIZE,
}: {
  token: string;
  size?: number;
}) {
  const { colors, radius } = useTheme();
  const [failed, setFailed] = useState(false);
  const config = DEFAULT_TOKENS[token];
  const source = token === 'MCGP' ? MCGP_LOGO : config?.iconUrl ? { uri: config.iconUrl } : null;

  if (source && !failed) {
    return (
      <Image
        source={source}
        onError={() => setFailed(true)}
        accessibilityIgnoresInvertColors
        style={{ width: size, height: size, borderRadius: radius.pill, backgroundColor: colors.surfaceAlt }}
      />
    );
  }
  const tint = config?.iconColor ?? colors.accent;
  return (
    <View
      style={{
        width: size,
        height: size,
        borderRadius: radius.pill,
        borderWidth: 2,
        borderColor: tint,
        alignItems: 'center',
        justifyContent: 'center',
      }}
    >
      <Text variant="bodyMed" weight="700" style={{ color: tint }}>
        {token.charAt(0)}
      </Text>
    </View>
  );
}
