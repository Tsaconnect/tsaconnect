// The app's one token icon. Logo from utils/tokenLogo (explicit logo → MCGP's
// bundled image → registry), else a white letter on the token's colour — the
// same fallback when a logo URL fails to load, never a blank disc. An optional
// corner badge names the chain, skipped for a chain's own coin (BNB on BNB
// Chain). Use this for every token or coin icon; don't hand-roll another.
import React, { useState } from 'react';
import { Image, View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from '@/components/ui';
import { DEFAULT_TOKENS, getChainDisplay, type AnyChainKey } from '@/constants/chains';
import { tokenLogoSource } from '@/utils/tokenLogo';

export default function TokenAvatar({
  symbol,
  iconUrl,
  iconColor,
  chainKey,
  curated = true,
  preferRegistry = false,
  size = 36,
}: {
  symbol: string;
  /** A logo for this exact token (LiFi logoURI, listing logoUrl, indexer thumbnail). */
  iconUrl?: string | null;
  iconColor?: string;
  /** Adds a corner badge with this chain's logo. */
  chainKey?: AnyChainKey;
  /** False for discovered/imported contracts: never borrow a registry logo by symbol. */
  curated?: boolean;
  /** Our own icon for a known symbol beats iconUrl (a coin shown across chains). */
  preferRegistry?: boolean;
  size?: number;
}) {
  const { colors } = useTheme();
  const source = tokenLogoSource(symbol, { explicitUri: iconUrl, curated, preferRegistry });
  const sourceKey = source === null ? '' : typeof source === 'number' ? String(source) : source.uri;
  const chain = chainKey ? getChainDisplay(chainKey) : undefined;
  const chainLogo = chain?.iconUrl ?? '';

  // Remember WHICH logo failed, not just that one did: pickers reuse an
  // instance across rows, and the next token's logo deserves its own attempt.
  const [failedLogo, setFailedLogo] = useState('');
  const [failedBadge, setFailedBadge] = useState('');
  const showLogo = source !== null && failedLogo !== sourceKey;
  const showBadgeLogo = chainLogo !== '' && failedBadge !== chainLogo;

  const showBadge = !!chain && chain.nativeCurrency.symbol.toUpperCase() !== symbol.toUpperCase();
  const badge = Math.max(12, Math.round(size * 0.42));
  const tint = iconColor || (curated ? DEFAULT_TOKENS[symbol.toUpperCase()]?.iconColor : undefined) || colors.textMuted;

  return (
    <View style={{ width: size, height: size }}>
      {showLogo ? (
        <Image
          testID="token-avatar-logo"
          source={source}
          onError={() => setFailedLogo(sourceKey)}
          accessibilityIgnoresInvertColors
          style={{ width: size, height: size, borderRadius: size / 2, backgroundColor: colors.surfaceAlt }}
        />
      ) : (
        <View
          testID="token-avatar-letter"
          style={{
            width: size,
            height: size,
            borderRadius: size / 2,
            backgroundColor: tint,
            alignItems: 'center',
            justifyContent: 'center',
          }}
        >
          <Text variant="bodyMed" weight="700" style={{ color: '#FFFFFF', fontSize: size * 0.4 }}>
            {symbol.trim().charAt(0).toUpperCase() || '?'}
          </Text>
        </View>
      )}
      {showBadge && chain ? (
        <View
          testID="token-avatar-chain"
          style={{
            position: 'absolute',
            right: -2,
            bottom: -2,
            width: badge,
            height: badge,
            borderRadius: badge / 2,
            borderWidth: 2,
            borderColor: colors.surface,
            backgroundColor: chain.iconColor,
            overflow: 'hidden',
          }}
        >
          {showBadgeLogo ? (
            <Image
              source={{ uri: chainLogo }}
              onError={() => setFailedBadge(chainLogo)}
              accessibilityIgnoresInvertColors
              style={{ width: '100%', height: '100%' }}
            />
          ) : null}
        </View>
      ) : null}
    </View>
  );
}
