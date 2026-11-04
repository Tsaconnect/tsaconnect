// Seller/counterparty avatar — image or generated initials, with an optional online dot.
import React from 'react';
import { Image, View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from './Text';

export interface AvatarProps {
  name?: string;
  uri?: string;
  size?: number;
  online?: boolean;
  /** 'auto' varies the tint per name (sellers); 'accent' is the brand tint, for the signed-in user. */
  tone?: 'auto' | 'accent';
}

function initials(name?: string): string {
  if (!name) return '?';
  const parts = name.trim().split(/\s+/).slice(0, 2);
  return parts.map((p) => p[0]?.toUpperCase() ?? '').join('') || '?';
}

// Deterministic accent tint from the name (stable color per seller).
function tintIndex(name = '', n: number): number {
  let h = 0;
  for (let i = 0; i < name.length; i++) h = (h * 31 + name.charCodeAt(i)) % 997;
  return h % n;
}

export const Avatar: React.FC<AvatarProps> = ({ name, uri, size = 40, online, tone = 'auto' }) => {
  const { colors } = useTheme();
  const tints = [colors.accentMuted, colors.infoBg, colors.successBg, colors.escrowBg, colors.warningBg];
  const fgs = [colors.accent, colors.info, colors.success, colors.escrow, colors.warning];
  const idx = tone === 'accent' ? 0 : tintIndex(name, tints.length);
  const dot = Math.max(8, Math.round(size * 0.28));

  return (
    <View style={{ width: size, height: size }}>
      {uri ? (
        <Image source={{ uri }} style={{ width: size, height: size, borderRadius: size / 2 }} />
      ) : (
        <View
          style={{
            width: size,
            height: size,
            borderRadius: size / 2,
            backgroundColor: tints[idx],
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          <Text variant="bodyMed" weight="700" style={{ color: fgs[idx], fontSize: size * 0.4 }}>
            {initials(name)}
          </Text>
        </View>
      )}
      {online && (
        <View
          style={{
            position: 'absolute',
            right: -1,
            bottom: -1,
            width: dot,
            height: dot,
            borderRadius: dot / 2,
            backgroundColor: colors.online,
            borderWidth: 2,
            borderColor: colors.surface,
          }}
        />
      )}
    </View>
  );
};
