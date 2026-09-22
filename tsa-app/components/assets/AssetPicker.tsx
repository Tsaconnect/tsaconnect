// components/assets/AssetPicker.tsx
//
// The one coin picker for the app: a short list of coins (On TSA, stablecoins
// and majors, what you hold), search by name or contract address for the rest,
// and — only for a coin on several networks — a second step to choose the
// network. Reused by All Swap and Buy with Naira; it loads its own data, so a
// screen only says which side it is and what to do with the choice.
//
// Contract with the screen is unchanged from the old TokenPickerModal:
// onSelect(token, chainId).

import React, { useEffect, useState } from 'react';
import { ScrollView, View } from 'react-native';
import { useTheme } from '@/theme';
import { useCurrency } from '@/contexts/CurrencyContext';
import { Icon, Input, PressableScale, Sheet, Spinner, Text } from '@/components/ui';
import TokenAvatar from '@/components/wallet/TokenAvatar';
import { getChainByChainId, NONEVM_CHAINS, type AnyChainKey } from '@/constants/chains';
import {
  getLiFiChainsCached, isBtcLiFiChain, isSolanaLiFiChain, isSuiLiFiChain, type LiFiChain, type LiFiToken,
} from '@/services/lifi';
import { networksForSide, type Asset, type AssetNetwork, type PickerSide } from '@/services/assetCatalog';
import { useAssetCatalog } from '@/hooks/useAssetCatalog';

export interface AssetPickerProps {
  visible: boolean;
  /** 'from' = paying with it (signable networks only); 'to' = receiving it. */
  side: PickerSide;
  title?: string;
  /** Optional extra limit on networks, e.g. what a buy can deliver to. */
  allow?: (chainId: number) => boolean;
  onSelect: (token: LiFiToken, chainId: number) => void;
  onClose: () => void;
}

// LiFi's chain list names the chains the wallet registry does not (the "to"
// side can be any of them). Shared with search through getLiFiChainsCached.
function useLiFiChainNames(enabled: boolean) {
  const [chains, setChains] = useState<LiFiChain[]>([]);
  useEffect(() => {
    if (!enabled) return;
    let cancelled = false;
    getLiFiChainsCached().then(c => { if (!cancelled) setChains(c); }).catch(() => {});
    return () => { cancelled = true; };
  }, [enabled]);
  return chains;
}

function chainKeyFor(chainId: number): AnyChainKey | undefined {
  if (isSolanaLiFiChain(chainId)) return 'solana';
  if (isBtcLiFiChain(chainId)) return 'btc';
  if (isSuiLiFiChain(chainId)) return 'sui';
  return getChainByChainId(chainId)?.key;
}

function chainName(chainId: number, lifi: LiFiChain[]): string {
  const key = chainKeyFor(chainId);
  if (key === 'solana' || key === 'btc' || key === 'sui') return NONEVM_CHAINS[key].name;
  return getChainByChainId(chainId)?.name ?? lifi.find(c => c.id === chainId)?.name ?? `Chain ${chainId}`;
}

/**
 * What TokenAvatar should draw for a coin: a trusted coin we know (USDC, ETH,
 * S, SOL…) gets our own icon, the same as in the wallet, over whichever chain's
 * LiFi logo came back. An untrusted coin never borrows one.
 */
const iconFor = (asset: Asset) =>
  ({ iconUrl: asset.logoURI, curated: asset.logoTrusted, preferRegistry: asset.logoTrusted });

const fmtAmount = (n: number) => n.toLocaleString('en-US', { maximumFractionDigits: n < 1 ? 6 : 4 });

export default function AssetPicker({ visible, side, title, allow, onSelect, onClose }: AssetPickerProps) {
  const { colors, spacing, radius } = useTheme();
  const { formatPrice } = useCurrency();
  const [query, setQuery] = useState('');
  const [networkFor, setNetworkFor] = useState<Asset | null>(null);
  const lifi = useLiFiChainNames(visible);
  const { sections, loading, searching, error } = useAssetCatalog({ visible, side, allow, query });

  // Every open starts clean.
  useEffect(() => {
    if (visible) { setQuery(''); setNetworkFor(null); }
  }, [visible]);

  const choose = (n: AssetNetwork) => {
    setNetworkFor(null);
    onSelect({ ...n.token, chainId: n.chainId }, n.chainId);
  };

  const tapAsset = (asset: Asset) => {
    const nets = networksForSide(asset, side, allow);
    if (nets.length === 1) choose(nets[0]);
    else setNetworkFor(asset);
  };

  const balanceBlock = (balance: number, usdValue: number, symbol: string) =>
    balance > 0 ? (
      <View style={{ alignItems: 'flex-end' }}>
        <Text variant="bodySm" weight="700" numeric>{fmtAmount(balance)} {symbol}</Text>
        {usdValue > 0 ? <Text variant="caption" color="textMuted" numeric>{formatPrice(usdValue)}</Text> : null}
      </View>
    ) : null;

  // ── Step 2: choose network ────────────────────────────────────────────────
  if (networkFor) {
    const nets = networksForSide(networkFor, side, allow);
    return (
      <Sheet visible={visible} onClose={onClose} onBack={() => setNetworkFor(null)} title={`${networkFor.symbol} · choose network`} maxHeightRatio={0.9}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.sm }}>
          <Text variant="bodySm" color="textSecondary">
            Same {networkFor.symbol}, different network.{side === 'from' ? ' Pick the one you hold it on.' : ' The recommended one costs least to use.'}
          </Text>
          {nets.map((n, i) => {
            const key = chainKeyFor(n.chainId);
            return (
              <PressableScale
                key={n.chainId}
                onPress={() => choose(n)}
                accessibilityRole="button"
                accessibilityLabel={`${networkFor.symbol} on ${chainName(n.chainId, lifi)}${i === 0 ? ', recommended' : ''}`}
                style={{
                  flexDirection: 'row', alignItems: 'center', gap: spacing.md, padding: spacing.md,
                  borderRadius: radius.lg, borderWidth: 1.5, borderColor: i === 0 ? colors.accent : colors.border,
                  backgroundColor: colors.surface,
                }}
              >
                <TokenAvatar symbol={networkFor.symbol} {...iconFor(networkFor)} chainKey={key} size={36} />
                <View style={{ flex: 1 }}>
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                    <Text variant="bodyMed" weight="700">{chainName(n.chainId, lifi)}</Text>
                    {i === 0 ? (
                      <View style={{ backgroundColor: colors.successBg, borderRadius: radius.pill, paddingHorizontal: 6, paddingVertical: 1 }}>
                        <Text variant="caption" weight="700" color="success">Recommended</Text>
                      </View>
                    ) : null}
                  </View>
                </View>
                {balanceBlock(n.balance ?? 0, n.usdValue ?? 0, networkFor.symbol)}
                <Icon name="chevronRight" size={16} color={colors.textMuted} />
              </PressableScale>
            );
          })}
        </ScrollView>
      </Sheet>
    );
  }

  // ── Step 1: choose coin ───────────────────────────────────────────────────
  const isSearch = query.trim().length >= 2;
  return (
    <Sheet visible={visible} onClose={onClose} title={title ?? (side === 'from' ? 'You pay with' : 'You receive')} maxHeightRatio={0.9}>
      <View style={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.sm }}>
        <Input
          value={query}
          onChangeText={setQuery}
          placeholder="Search name or paste contract address"
          autoCapitalize="none"
          autoCorrect={false}
          accessibilityLabel="Search coins"
          leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
        />
      </View>
      <ScrollView contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingBottom: spacing['2xl'] }} keyboardShouldPersistTaps="handled">
        {error ? <Text variant="bodySm" color="danger" style={{ marginVertical: spacing.md }}>{error}</Text> : null}
        {(loading && !isSearch) || searching ? (
          <View style={{ paddingVertical: spacing.xl }}><Spinner label={searching ? 'Searching…' : 'Loading coins…'} /></View>
        ) : null}

        {sections.map(section => (
          <View key={section.id} style={{ marginTop: spacing.md }}>
            <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.xs }}>{section.title}</Text>
            {section.id === 'search' && section.assets.some(a => !a.verified) ? (
              <View style={{ backgroundColor: colors.warningBg, borderRadius: radius.md, padding: spacing.sm, marginBottom: spacing.xs }}>
                <Text variant="caption" style={{ color: colors.warning }}>
                  Anyone can create a token. Check the contract address before you trade an unverified one.
                </Text>
              </View>
            ) : null}
            {section.assets.map(asset => {
              const nets = networksForSide(asset, side, allow);
              const sub = nets.length > 1 ? `${nets.length} networks` : chainName(nets[0].chainId, lifi);
              return (
                <PressableScale
                  key={asset.key}
                  onPress={() => tapAsset(asset)}
                  accessibilityRole="button"
                  accessibilityLabel={`${asset.symbol}, ${sub}${asset.balance > 0 ? `, you hold ${fmtAmount(asset.balance)}` : ''}`}
                  style={{
                    flexDirection: 'row', alignItems: 'center', gap: spacing.md, paddingVertical: spacing.md,
                    borderBottomWidth: 1, borderBottomColor: colors.hairline,
                  }}
                >
                  <TokenAvatar
                    symbol={asset.symbol}
                    {...iconFor(asset)}
                    chainKey={nets.length === 1 ? chainKeyFor(nets[0].chainId) : undefined}
                    size={40}
                  />
                  <View style={{ flex: 1, minWidth: 0 }}>
                    <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, flexWrap: 'wrap' }}>
                      <Text variant="bodyMed" weight="700" numberOfLines={1}>{asset.symbol}</Text>
                      {section.id === 'tsa' ? <Pill label="TSA" fg={colors.accent} bg={colors.accentMuted} /> : null}
                      {section.id === 'search' && !asset.verified ? <Pill label="Unverified" fg={colors.warning} bg={colors.warningBg} /> : null}
                    </View>
                    <Text variant="caption" color="textSecondary" numberOfLines={1}>{asset.name} · {sub}</Text>
                  </View>
                  {balanceBlock(asset.balance, asset.usdValue, asset.symbol)}
                </PressableScale>
              );
            })}
          </View>
        ))}

        {isSearch && !searching && !error && sections.length === 0 ? (
          <Text variant="bodySm" color="textMuted" align="center" style={{ marginTop: spacing.xl }}>
            No coin matches that. Try pasting its contract address.
          </Text>
        ) : null}
        {!isSearch && !loading ? (
          <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.lg }}>
            Looking for another coin? Search by name or paste its contract address.
          </Text>
        ) : null}
      </ScrollView>
    </Sheet>
  );
}

function Pill({ label, fg, bg }: { label: string; fg: string; bg: string }) {
  const { radius } = useTheme();
  return (
    <View style={{ backgroundColor: bg, borderRadius: radius.pill, paddingHorizontal: 6, paddingVertical: 1 }}>
      <Text variant="caption" weight="700" style={{ color: fg }}>{label}</Text>
    </View>
  );
}
