// screens/wallet.tsx
//
// Multi-chain wallet home — total balance hero, quick actions, asset list with
// chain filter, recent activity, and the deposit / withdraw / wallet-selector /
// manage-tokens sheets. Deposit and Withdraw each list every live rail
// (Instant Transfer, Individual Payment, Payment Merchant, plain crypto) —
// names come from constants/rails.ts. Migrated onto the TSA Signature design system
// (theme tokens + @/components/ui primitives). All data fetching, state,
// effects (balance fetch + race guard, discovery/custom-token merges,
// AsyncStorage hidden-tokens), navigation routes/params, money handling, and
// network switching are unchanged — this is a visual/structural restyle only.
import { router, useFocusEffect, useLocalSearchParams } from 'expo-router';
import ActivityFeed from '@/components/activity/ActivityFeed';
import React, { useState, useEffect, useCallback, useRef, useMemo } from 'react';
import {
  View,
  ScrollView,
  StyleSheet,
  RefreshControl,
  Modal,
  Image,
  TextInput,
  FlatList,
} from 'react-native';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { SafeAreaView, useSafeAreaInsets } from 'react-native-safe-area-context';
import {
  getWalletBalances,
  normalizeWalletBalances,
  registerWalletAddress,
  getDiscoveredTokens,
  type DiscoveredToken,
} from '../services/walletApi';
import {
  getWalletList,
  getActiveWallet,
  setActiveWallet,
  migrateFromSingleWallet,
  readErc20BalanceOf,
  formatTokenBalance,
  WalletMeta,
} from '../services/wallet';
import { useTokens, type CustomToken } from '../hooks/useTokens';
import {
  CHAINS, NONEVM_CHAIN_KEYS, getChainDisplay, isNonEVMChain, tokenRankOf,
  type ChainKey, type AnyChainKey,
} from '../constants/chains';
import { loadTronWallet } from '../services/chains/tronWallet';
import { loadBtcWallet } from '../services/chains/btcWallet';
import { loadSolanaWallet } from '../services/chains/solanaWallet';
import { loadSuiWallet } from '../services/chains/suiWallet';
import { getPremiumMcgpAsset } from '../services/mcgpSwapApi';
import { type SpotCategory } from '../services/spotApi';
import { assetCategoryOf, categoryBySymbol, WALLET_CATEGORY_FILTERS } from '../utils/spotTabs';
import {
  allSourcesReported,
  combineSourceAssets,
  emptySourceAssets,
  type AssetSource,
  type SourceAssets,
} from '../utils/walletAssets';
import { useSpotListings } from '../hooks/useSpotListings';
import { useNetwork } from '../hooks/useNetwork';
import { RAILS, RAIL_ROUTES, railLabel } from '../constants/rails';
import { ethers } from 'ethers';
import { useTheme } from '@/theme';
import {
  AccentCard,
  AmountText,
  Avatar,
  Badge,
  Button,
  Divider,
  EmptyState,
  Icon,
  IconButton,
  PressableScale,
  Sheet,
  Skeleton,
  Text,
  Toggle,
} from '@/components/ui';

// ── Types ──
interface WalletAsset {
  id: string;
  symbol: string;
  name: string;
  balance: number;
  usdValue: number;
  usdPrice: number;
  iconColor: string;
  iconUrl?: string;
  /** Set on rows surfaced by the indexer instead of supported_tokens. */
  discovered?: boolean;
  /** Short label chip (e.g. "Spendable" for premium MCGP). */
  tag?: string;
  /** ERC-20 contract address; required for discovered rows so Send works. */
  contractAddress?: string;
  /** Token decimals; required for discovered rows so amount→wei conversion is correct. */
  decimals?: number;
  details?: {
    type: string;
    chain: string;
    chainKey: AnyChainKey;
  };
}

// hashColor produces a stable HSL color from a string — used as a deterministic
// avatar fallback for discovered tokens we don't have an icon for.
function hashColor(str: string): string {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  return `hsl(${Math.abs(hash) % 360}, 55%, 50%)`;
}

function getManageTokenMeta(asset: WalletAsset): string {
  const name = asset.name?.trim() || '';
  const chain = asset.details?.chain?.trim() || '';

  if (!name) return chain;
  if (!chain || name.toLowerCase() === chain.toLowerCase()) return name;
  return `${name} · ${chain}`;
}

// mergeDiscoveredTokens fetches the auto-discovery list and returns the rows
// that aren't already in `existing` (matched by chainKey+symbol or
// chainKey+contract). Returns ONLY the additions, never the combined list: the
// caller holds the per-source rows separately and a stale combined list would
// shadow balances that had since been refreshed. Empty when there is nothing to
// add or the indexer is offline.
async function mergeDiscoveredTokens(
  existing: WalletAsset[],
  address: string | undefined,
): Promise<WalletAsset[]> {
  try {
    const result = await getDiscoveredTokens(undefined, address);
    if (!result.success || !result.data || !result.data.providerAvailable) {
      return [];
    }
    const discovered: DiscoveredToken[] = result.data.tokens || [];
    if (discovered.length === 0) return [];

    // Index existing rows by chainKey+symbol AND chainKey+contract so we
    // don't duplicate either USDC-on-base (matched by symbol) or a custom
    // token previously imported (matched by address).
    const seenSymbol = new Set(
      existing
        .filter((a) => a.details?.chainKey)
        .map((a) => `${a.details!.chainKey}::${a.symbol.toUpperCase()}`),
    );
    const seenContract = new Set(
      existing
        .filter((a) => a.contractAddress && a.details?.chainKey)
        .map((a) => `${a.details!.chainKey}::${a.contractAddress!.toLowerCase()}`),
    );

    let nextId = existing.length + 1;
    const additions: WalletAsset[] = [];
    for (const t of discovered) {
      const chainKey = t.chain as ChainKey;
      const chain = CHAINS[chainKey];
      if (!chain) continue;
      const symbolKey = `${chainKey}::${t.symbol.toUpperCase()}`;
      const contractKey = `${chainKey}::${t.contractAddress.toLowerCase()}`;
      if (seenSymbol.has(symbolKey) || seenContract.has(contractKey)) continue;

      const balance = formatTokenBalance(t.balance, t.decimals);
      const usdValue = Number.isFinite(t.usdValue) ? t.usdValue : 0;
      additions.push({
        id: String(nextId++),
        symbol: t.symbol,
        name: t.name || t.symbol,
        balance,
        usdValue,
        usdPrice: balance > 0 && usdValue > 0 ? usdValue / balance : 0,
        iconColor: hashColor(t.symbol),
        discovered: true,
        contractAddress: t.contractAddress,
        decimals: t.decimals,
        details: { type: 'Discovered', chain: chain.name, chainKey },
      });
    }
    return additions;
  } catch (err) {
    console.warn('[wallet] discovery merge failed:', err);
    return [];
  }
}

// mergeCustomTokens surfaces user-imported tokens in the asset list and tries
// to attach an on-chain balance for each. We always add the token (with
// balance 0 if the read fails) so the user can see what they imported — public
// RPCs go down often enough that silently dropping the row makes imports look
// "lost".
async function mergeCustomTokens(
  existing: WalletAsset[],
  address: string | undefined,
  customTokens: CustomToken[],
): Promise<WalletAsset[]> {
  if (customTokens.length === 0) return [];
  try {
    const seenContract = new Set(
      existing
        .filter((a) => a.contractAddress && a.details?.chainKey)
        .map((a) => `${a.details!.chainKey}::${a.contractAddress!.toLowerCase()}`),
    );

    const balances = address
      ? await Promise.all(
          customTokens.map(async (t) => {
            // Non-EVM custom tokens (e.g. imported SPL mints) have no ERC-20
            // balanceOf; their balance is already reflected by the nonevm
            // loaders' auto-discovery and dedups against `existing` below.
            if (isNonEVMChain(t.chainKey)) return null;
            try {
              const raw = await readErc20BalanceOf(t.contractAddress, address, t.chainKey);
              return Number(ethers.formatUnits(raw, t.decimals));
            } catch (err) {
              console.warn(
                `[wallet] balanceOf failed for custom ${t.symbol} on ${t.chainKey}:`,
                err,
              );
              return null;
            }
          }),
        )
      : customTokens.map(() => null);

    let nextId = existing.length + 1;
    const additions: WalletAsset[] = [];

    customTokens.forEach((token, i) => {
      const contractKey = `${token.chainKey}::${token.contractAddress.toLowerCase()}`;
      if (seenContract.has(contractKey)) return;
      // getChainDisplay resolves both EVM and non-EVM chains (CHAINS is EVM-only).
      const chain = getChainDisplay(token.chainKey);
      if (!chain) return;

      const balance = balances[i] ?? 0;

      additions.push({
        id: String(nextId++),
        symbol: token.symbol,
        name: token.name,
        balance,
        usdValue: 0,
        usdPrice: 0,
        iconColor: token.iconColor || hashColor(token.symbol),
        iconUrl: token.iconUrl,
        contractAddress: token.contractAddress,
        decimals: token.decimals,
        details: { type: 'Imported', chain: chain.name, chainKey: token.chainKey },
      });
    });

    return additions;
  } catch (err) {
    console.warn('[wallet] custom-token merge failed:', err);
    return [];
  }
}

// ── Quick Actions ──
const QUICK_ACTIONS = [
  { key: 'fund', label: 'Deposit', icon: 'bank' as const },
  { key: 'swap', label: 'Swap', icon: 'swap' as const },
  { key: 'send', label: 'Withdraw', icon: 'send' as const },
  { key: 'instant-pay', label: 'Instant Pay', icon: 'bolt' as const },
] as const;

// ── Balance Card ──
const WalletBalanceCard: React.FC<{
  totalUsdValue: number;
  /**
   * True while any balance source is still in flight. The total is the SUM of
   * every chain, so a partial sum is not a smaller number — it is a wrong one.
   * A user glancing at $1,200 while Bitcoin is still loading reads it as their
   * balance. Shimmer until the figure is real.
   */
  isTotalPending: boolean;
  isValuesHidden: boolean;
  activeLabel: string;
  onToggleVisibility: () => void;
  onQuickAction: (action: string) => void;
  onWalletPress: () => void;
}> = ({ totalUsdValue, isTotalPending, isValuesHidden, activeLabel, onToggleVisibility, onQuickAction, onWalletPress }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <AccentCard padding="xl">
      <PressableScale
        onPress={onWalletPress}
        style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs, alignSelf: 'flex-start' }}
      >
        <Text variant="bodySm" weight="600" color="textSecondary">
          {activeLabel}
        </Text>
        <Icon name="chevronDown" size={16} color={colors.textMuted} />
      </PressableScale>

      <View
        style={{
          flexDirection: 'row',
          justifyContent: 'space-between',
          alignItems: 'center',
          marginTop: spacing.md,
        }}
      >
        <Text variant="bodySm" color="textSecondary">
          Wallet Balance
        </Text>
        <IconButton
          icon={isValuesHidden ? 'lock' : 'wallet'}
          size={18}
          color={colors.textMuted}
          accessibilityLabel={isValuesHidden ? 'Show balance' : 'Hide balance'}
          onPress={onToggleVisibility}
        />
      </View>

      {isValuesHidden ? (
        <Text variant="display" weight="700">
          ••••••
        </Text>
      ) : isTotalPending ? (
        <Skeleton height={40} width={180} radius="sm" />
      ) : (
        <AmountText value={totalUsdValue} currency="USD" variant="display" />
      )}

      <Divider spacing="lg" />

      <View style={{ flexDirection: 'row', justifyContent: 'space-around' }}>
        {QUICK_ACTIONS.map((action) => (
          <PressableScale
            key={action.key}
            style={{ alignItems: 'center', minWidth: 56 }}
            onPress={() => onQuickAction(action.key)}
          >
            <View
              style={{
                width: 44,
                height: 44,
                borderRadius: radius.pill,
                backgroundColor: colors.accentMuted,
                justifyContent: 'center',
                alignItems: 'center',
                marginBottom: spacing.xs,
              }}
            >
              <Icon name={action.icon} size={20} color={colors.accent} />
            </View>
            <Text variant="caption" color="textSecondary">
              {action.label}
            </Text>
          </PressableScale>
        ))}
      </View>
    </AccentCard>
  );
};

// ── Asset Row ──
const AssetRow: React.FC<{
  asset: WalletAsset;
  isValuesHidden: boolean;
  onPress?: () => void;
}> = ({ asset, isValuesHidden, onPress }) => {
  const { colors, spacing } = useTheme();
  const isNative = asset.details?.type === 'Native Token';
  return (
    <PressableScale
      style={{ flexDirection: 'row', alignItems: 'center', paddingVertical: spacing.sm }}
      onPress={onPress}
    >
      <View style={{ marginRight: spacing.md }}>
        <Avatar name={asset.symbol} uri={asset.iconUrl} size={40} />
      </View>
      <View style={{ flex: 1 }}>
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
          <Text variant="bodyMed" weight="600">
            {asset.symbol}
          </Text>
          {asset.discovered && <Badge label="NEW" size="sm" color={colors.accent} background={colors.accentMuted} />}
          {asset.tag && <Badge label={asset.tag} size="sm" color={colors.warning} background={colors.warningBg} />}
        </View>
        <View style={{ flexDirection: 'row', alignItems: 'center', marginTop: 2 }}>
          <Text variant="caption" color="textMuted">
            {asset.name}
          </Text>
          {asset.details?.chain && (
            <>
              <View
                style={{
                  width: 5,
                  height: 5,
                  borderRadius: 2.5,
                  marginLeft: spacing.xs,
                  marginRight: 3,
                  backgroundColor: getChainDisplay(asset.details.chainKey)?.iconColor || colors.textMuted,
                }}
              />
              <Text variant="caption" color="textMuted">
                {asset.details.chain}
              </Text>
            </>
          )}
        </View>
      </View>
      <View style={{ alignItems: 'flex-end' }}>
        <Text variant="bodyMed" weight="600">
          {isValuesHidden ? '••••' : asset.balance.toFixed(isNative ? 4 : 2)}
        </Text>
        <Text variant="caption" color="textMuted" style={{ marginTop: 2 }}>
          {isValuesHidden
            ? '••••'
            : `$${asset.usdValue.toLocaleString(undefined, { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`}
        </Text>
      </View>
    </PressableScale>
  );
};

// ── Deposit Sheet ──
// Every live deposit rail: Instant Transfer (fiat in, bank rails), the two
// counterparty markets (individual and merchant), and a plain crypto receive.
const FundModal: React.FC<{ visible: boolean; onClose: () => void }> = ({ visible, onClose }) => {
  const go = (route: string) => {
    onClose();
    router.push(route as any);
  };
  return (
    <Sheet visible={visible} onClose={onClose} title="Deposit into Your Wallet">
      <View style={{ paddingHorizontal: 16, paddingTop: 16 }}>
        <SheetOption
          icon="bolt"
          title={railLabel('it')}
          desc={RAILS.it.depositDesc}
          onPress={() => go(RAIL_ROUTES.itBuy)}
        />
        <SheetOption
          icon="user"
          title={railLabel('p2p')}
          desc={RAILS.p2p.depositDesc}
          onPress={() => go(RAIL_ROUTES.p2pBuy)}
        />
        <SheetOption
          icon="storefront"
          title={railLabel('pm')}
          desc={RAILS.pm.depositDesc}
          onPress={() => go(RAIL_ROUTES.pmBuy)}
        />
        <SheetOption
          icon="wallet"
          title="Crypto Deposit"
          desc="From an external wallet or exchange"
          onPress={() => go('/fund')}
        />
      </View>
    </Sheet>
  );
};

// ── Withdraw Sheet ──
const SendModal: React.FC<{ visible: boolean; onClose: () => void }> = ({ visible, onClose }) => {
  const go = (route: string) => {
    onClose();
    router.push(route as any);
  };
  return (
    <Sheet visible={visible} onClose={onClose} title="Withdraw / Send">
      <View style={{ paddingHorizontal: 16, paddingTop: 16 }}>
        <SheetOption
          icon="bolt"
          title={railLabel('it')}
          desc={RAILS.it.withdrawDesc}
          onPress={() => go(RAIL_ROUTES.itSell)}
        />
        <SheetOption
          icon="user"
          title={railLabel('p2p')}
          desc={RAILS.p2p.withdrawDesc}
          onPress={() => go(RAIL_ROUTES.p2pSell)}
        />
        <SheetOption
          icon="storefront"
          title={railLabel('pm')}
          desc={RAILS.pm.withdrawDesc}
          onPress={() => go(RAIL_ROUTES.pmSell)}
        />
        <SheetOption
          icon="wallet"
          title="Send Crypto"
          desc="To an external wallet or exchange"
          onPress={() => go('/wallet/send')}
        />
      </View>
    </Sheet>
  );
};

// Shared sheet option row (icon tile + title/desc + chevron).
const SheetOption: React.FC<{
  icon: React.ComponentProps<typeof Icon>['name'];
  title: string;
  desc: string;
  onPress: () => void;
  iconColor?: string;
  iconBg?: string;
  trailing?: React.ReactNode;
}> = ({ icon, title, desc, onPress, iconColor, iconBg, trailing }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <PressableScale
      onPress={onPress}
      style={{
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.md,
        padding: spacing.md,
        backgroundColor: colors.surfaceAlt,
        borderRadius: radius.lg,
        marginBottom: spacing.sm,
      }}
    >
      <View
        style={{
          width: 48,
          height: 48,
          borderRadius: radius.md,
          justifyContent: 'center',
          alignItems: 'center',
          backgroundColor: iconBg ?? colors.accentMuted,
        }}
      >
        <Icon name={icon} size={22} color={iconColor ?? colors.accent} />
      </View>
      <View style={{ flex: 1 }}>
        <Text variant="bodyMed" weight="600">
          {title}
        </Text>
        <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 2 }}>
          {desc}
        </Text>
      </View>
      {trailing ?? <Icon name="chevronRight" size={18} color={colors.textMuted} />}
    </PressableScale>
  );
};

// ── Manage Tokens Modal ──
const ManageTokensModal: React.FC<{
  visible: boolean;
  assets: WalletAsset[];
  hiddenTokens: Set<string>;
  tokenKey: (chainKey: string, symbol: string, contractAddress?: string) => string;
  onToggle: (key: string) => void;
  onClose: () => void;
  onImport: () => void;
}> = ({ visible, assets, hiddenTokens, tokenKey, onToggle, onClose, onImport }) => {
  const [search, setSearch] = useState('');
  const [chainFilter, setChainFilter] = useState<'all' | AnyChainKey>('all');
  const insets = useSafeAreaInsets();
  const { colors, spacing, radius, fonts, type } = useTheme();

  const filtered = useMemo(() => {
    const q = search.toLowerCase().trim();
    return assets.filter(a => {
      if (chainFilter !== 'all' && a.details?.chainKey !== chainFilter) return false;
      if (!q) return true;
      return a.symbol.toLowerCase().includes(q) || a.name.toLowerCase().includes(q);
    });
  }, [assets, search, chainFilter]);

  return (
    <Modal visible={visible} animationType="slide" onRequestClose={onClose}>
      <View style={{ flex: 1, backgroundColor: colors.background, paddingTop: insets.top, paddingBottom: insets.bottom }}>
        <View
          style={{
            minHeight: 56,
            flexDirection: 'row',
            alignItems: 'center',
            paddingHorizontal: spacing.lg,
            borderBottomWidth: StyleSheet.hairlineWidth,
            borderBottomColor: colors.hairline,
          }}
        >
          <View style={{ width: 104, justifyContent: 'center' }}>
            <IconButton icon="chevronLeft" accessibilityLabel="Back" onPress={onClose} style={{ marginLeft: -10 }} />
          </View>
          <Text
            variant="title3"
            weight="700"
            align="center"
            numberOfLines={1}
            style={{ flex: 1 }}
          >
            Manage crypto
          </Text>
          <View style={{ width: 104, alignItems: 'flex-end' }}>
            <PressableScale onPress={onImport} hitSlop={8}>
              <Text variant="bodySm" weight="700" color="accent" numberOfLines={1}>
                Import tokens
              </Text>
            </PressableScale>
          </View>
        </View>

        <ScrollView
          horizontal
          showsHorizontalScrollIndicator={false}
          style={{ flexGrow: 0, marginTop: spacing.md }}
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingVertical: spacing.xs, paddingRight: spacing['2xl'], alignItems: 'center', gap: spacing.sm }}
        >
          <ManageChip active={chainFilter === 'all'} label="All" onPress={() => setChainFilter('all')} />
          {(Object.entries(CHAINS) as [ChainKey, (typeof CHAINS)[ChainKey]][]).map(([key, chain]) => (
            <ManageChip
              key={key}
              active={chainFilter === key}
              label={chain.shortName}
              iconUrl={chain.iconUrl}
              iconColor={chain.iconColor}
              onPress={() => setChainFilter(key)}
            />
          ))}
          {NONEVM_CHAIN_KEYS.map((key) => {
            const chain = getChainDisplay(key);
            if (!chain) return null;
            return (
              <ManageChip
                key={key}
                active={chainFilter === key}
                label={chain.shortName}
                iconUrl={chain.iconUrl}
                iconColor={chain.iconColor}
                onPress={() => setChainFilter(key)}
              />
            );
          })}
        </ScrollView>

        <View
          style={{
            minHeight: 54,
            flexDirection: 'row',
            alignItems: 'center',
            gap: spacing.sm,
            marginHorizontal: spacing.lg,
            marginTop: spacing.sm,
            marginBottom: spacing.md,
            backgroundColor: colors.surfaceAlt,
            borderRadius: radius.md,
            paddingHorizontal: spacing.lg,
            paddingVertical: spacing.sm,
          }}
        >
          <Icon name="search" size={18} color={colors.textMuted} />
          <TextInput
            style={{ flex: 1, fontSize: type.body.fontSize, color: colors.textPrimary, fontFamily: fonts['400'], paddingVertical: 0 }}
            placeholder="Search tokens"
            placeholderTextColor={colors.textMuted}
            value={search}
            onChangeText={setSearch}
            autoCapitalize="none"
            autoCorrect={false}
          />
          {search.length > 0 && (
            <PressableScale onPress={() => setSearch('')} hitSlop={8}>
              <Icon name="close" size={18} color={colors.textMuted} />
            </PressableScale>
          )}
        </View>

        <FlatList
          data={filtered}
          keyExtractor={item => `${item.details?.chainKey}-${item.symbol}-${item.contractAddress || ''}`}
          keyboardShouldPersistTaps="handled"
          showsVerticalScrollIndicator={false}
          renderItem={({ item }) => {
            const key = tokenKey(item.details?.chainKey || '', item.symbol, item.contractAddress);
            const isVisible = !hiddenTokens.has(key);
            const meta = getManageTokenMeta(item);
            return (
              <PressableScale
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  paddingHorizontal: spacing.lg,
                  paddingVertical: spacing.sm,
                  borderBottomWidth: StyleSheet.hairlineWidth,
                  borderBottomColor: colors.hairline,
                }}
                onPress={() => onToggle(key)}
              >
                <View style={{ marginRight: spacing.md }}>
                  <Avatar name={item.symbol} uri={item.iconUrl} size={40} />
                </View>
                <View style={{ flex: 1 }}>
                  <Text variant="bodyMed" weight="600">
                    {item.symbol}
                  </Text>
                  {meta ? (
                    <Text variant="caption" color="textMuted" numberOfLines={1} style={{ marginTop: 2 }}>
                      {meta}
                    </Text>
                  ) : null}
                </View>
                <Toggle
                  value={isVisible}
                  onValueChange={() => onToggle(key)}
                  accessibilityLabel={`Toggle ${item.symbol}`}
                />
              </PressableScale>
            );
          }}
          contentContainerStyle={{ paddingTop: 2, paddingBottom: Math.max(insets.bottom, spacing.xl) + spacing.md }}
        />
      </View>
    </Modal>
  );
};

// Filter chip used inside the manage-tokens modal's horizontal rail.
const ManageChip: React.FC<{
  active: boolean;
  label: string;
  iconUrl?: string;
  iconColor?: string;
  onPress: () => void;
}> = ({ active, label, iconUrl, iconColor, onPress }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <PressableScale
      onPress={onPress}
      style={{
        flexShrink: 0,
        minHeight: 36,
        flexDirection: 'row',
        alignItems: 'center',
        gap: spacing.xs,
        paddingHorizontal: spacing.md,
        paddingVertical: spacing.xs,
        borderRadius: radius.pill,
        backgroundColor: active ? colors.accentMuted : colors.surface,
        borderWidth: 1,
        borderColor: active ? colors.accent : colors.border,
      }}
    >
      {iconUrl ? (
        <Image source={{ uri: iconUrl }} style={{ width: 16, height: 16, borderRadius: 8 }} resizeMode="cover" />
      ) : iconColor ? (
        <View style={{ width: 10, height: 10, borderRadius: 5, backgroundColor: iconColor }} />
      ) : null}
      <Text variant="bodySm" weight={active ? '700' : '500'} style={{ color: active ? colors.accent : colors.textSecondary }}>
        {label}
      </Text>
    </PressableScale>
  );
};

// ── Main Screen ──
const WalletScreen: React.FC = () => {
  const { colors, spacing, radius } = useTheme();
  const { tokenList, customTokens } = useTokens();
  const { network, switchNetwork } = useNetwork();
  // Assets are held PER SOURCE rather than as one array, because the sources
  // are independent network calls that finish at wildly different times (an
  // Esplora BTC read is nothing like the EVM fan-out). Keyed this way, each one
  // paints the moment it lands and a slow chain cannot hold up a fast one.
  // Combined in render order below, which preserves the previous ordering.
  const [sourceAssets, setSourceAssets] = useState<SourceAssets<WalletAsset>>(
    emptySourceAssets<WalletAsset>,
  );
  // Rows contributed by custom-token import and auto-discovery. Held apart from
  // the sources so a merge computed against an earlier snapshot can never
  // shadow a balance that has since been refreshed.
  const [extraAssets, setExtraAssets] = useState<WalletAsset[]>([]);
  // Which wallet the rows above belong to. A refetch for the SAME wallet keeps
  // showing them while it revalidates; only a different wallet clears them.
  const loadedForRef = useRef<string | null>(null);

  const assets = useMemo<WalletAsset[]>(
    () => combineSourceAssets(sourceAssets, extraAssets),
    [sourceAssets, extraAssets]
  );

  // A partial sum is a wrong number, not a smaller one — see WalletBalanceCard.
  // Only true before the FIRST complete load: on a revalidation the previous
  // total keeps showing rather than blanking every time the tab regains focus.
  const isTotalPending = !allSourcesReported(sourceAssets);
  const [isValuesHidden, setIsValuesHidden] = useState(false);
  const [isLoading, setIsLoading] = useState(true);
  const [isRefreshing, setIsRefreshing] = useState(false);
  const [showFundModal, setShowFundModal] = useState(false);
  const [showSendModal, setShowSendModal] = useState(false);
  const [walletList, setWalletList] = useState<WalletMeta[]>([]);
  const [activeLabel, setActiveLabel] = useState('Wallet');
  const [activeAddress, setActiveAddress] = useState('');
  const [showWalletSelector, setShowWalletSelector] = useState(false);
  const [hiddenTokens, setHiddenTokens] = useState<Set<string>>(new Set());
  const [showManageModal, setShowManageModal] = useState(false);
  const [selectedChain, setSelectedChain] = useState<'all' | AnyChainKey>('all');
  const [selectedCategory, setSelectedCategory] = useState<SpotCategory | 'all'>('all');
  const [showFilterSheet, setShowFilterSheet] = useState(false);

  // ?sheet=deposit|withdraw opens the matching rail sheet, so a shortcut
  // elsewhere in the app can land on the full list of rails instead of
  // preempting the choice by deep-linking into one of them. Keyed on the param
  // (not run on mount alone) because this tab stays mounted once visited.
  const { sheet } = useLocalSearchParams<{ sheet?: string }>();
  useEffect(() => {
    if (sheet === 'deposit') setShowFundModal(true);
    else if (sheet === 'withdraw') setShowSendModal(true);
  }, [sheet]);
  // SYMBOL → taxonomy category, from the shared listings cache. When the fetch
  // fails the map is empty and every asset falls back to "spot", which would
  // silently produce wrong CATEGORY results — so the sheet surfaces the error
  // rather than pretending the filter works.
  const { listings: spotListings, error: listingsError } = useSpotListings();
  const categoryMap = useMemo<Map<string, SpotCategory>>(
    () => categoryBySymbol(spotListings),
    [spotListings],
  );

  useEffect(() => {
    AsyncStorage.getItem('wallet_hidden_tokens').then(v => {
      if (v) setHiddenTokens(new Set(JSON.parse(v)));
    });
  }, []);

  const tokenKey = (chainKey: string, symbol: string, contractAddress?: string) =>
    `${chainKey}::${contractAddress || symbol}`;

  const toggleHidden = useCallback((key: string) => {
    setHiddenTokens(prev => {
      const next = new Set(prev);
      if (next.has(key)) next.delete(key); else next.add(key);
      AsyncStorage.setItem('wallet_hidden_tokens', JSON.stringify([...next]));
      return next;
    });
  }, []);

  // Each fetchBalances invocation increments this; the discovery .then
  // resolves only if its captured token still matches, so a slower
  // in-flight discovery from a previous fetch can't overwrite newer state
  // (e.g., user pulls to refresh while discovery is mid-flight).
  const fetchTokenRef = useRef(0);

  // Only native tokens form the skeleton — ERC-20s are added dynamically
  // from balance responses and discovery (balance > 0 only).
  const buildNativeAssets = useCallback((): WalletAsset[] => {
    return Object.entries(CHAINS).map(([chainKey, chain], i) => ({
      id: String(i + 1),
      symbol: chain.nativeCurrency.symbol,
      name: chain.name,
      balance: 0, usdValue: 0, usdPrice: 0,
      iconColor: chain.iconColor,
      iconUrl: chain.iconUrl,
      details: { type: 'Native Token', chain: chain.name, chainKey: chainKey as ChainKey },
    }));
  }, []);

  // ── Per-source loaders ────────────────────────────────────────────────────
  // Each returns only its own assets and touches no shared state, which is what
  // lets them run concurrently. Every one swallows its own failure: a chain
  // being unreachable must cost the user that chain's rows, not the whole
  // wallet.

  const loadEvmAssets = useCallback(async (): Promise<WalletAsset[]> => {
    const nativeAssets = buildNativeAssets();
    const result = await getWalletBalances();
    let updated: WalletAsset[] = nativeAssets;
    if (result.success && result.data) {
      const normalized = normalizeWalletBalances(result.data);
      const balanceMap = new Map(normalized.map(e => [`${e.symbol}-${e.chainKey}`, e] as const));

      // Overlay balances onto native tokens
      let id = nativeAssets.length + 1;
      updated = nativeAssets.map(asset => {
        const entry = balanceMap.get(`${asset.symbol}-${asset.details!.chainKey}`);
        if (!entry) return asset;
        const balance = parseFloat(entry.balance) || 0;
        const usdValue = parseFloat(entry.usdValue) || 0;
        return { ...asset, balance, usdValue, usdPrice: entry.usdPrice || (balance > 0 ? usdValue / balance : 0) };
      });

      // Add ERC-20s only if balance > 0
      for (const entry of normalized) {
        const bal = parseFloat(entry.balance);
        if (!bal || bal <= 0) continue;
        const isNative = nativeAssets.some(
          a => a.symbol === entry.symbol && a.details?.chainKey === entry.chainKey
        );
        if (isNative) continue;

        // EVM-only legacy view: balances come from the EVM fan-out, so
        // chainKey is always an EVM ChainKey here.
        const evmChainKey = entry.chainKey as ChainKey;
        const tokenConfig = tokenList.find(
          t => t.symbol === entry.symbol && t.chains.includes(evmChainKey)
        );
        const chain = CHAINS[evmChainKey];
        if (!chain) continue;

        updated.push({
          id: String(id++),
          symbol: entry.symbol,
          name: tokenConfig?.name || entry.name || entry.symbol,
          balance: bal,
          usdValue: parseFloat(entry.usdValue) || 0,
          usdPrice: entry.usdPrice || 0,
          iconColor: tokenConfig?.iconColor || '#888',
          iconUrl: tokenConfig?.iconUrl,
          contractAddress: entry.contractAddress || undefined,
          decimals: entry.decimals,
          details: {
            type: entry.symbol === 'MCGP' ? 'Gold-Backed Token' : 'Token',
            chain: chain.name,
            chainKey: evmChainKey,
          },
        });
      }
    }
    return updated;
  }, [buildNativeAssets, tokenList]);

  const loadTronAssets = useCallback(async (addr: string): Promise<WalletAsset[]> => {
    const out: WalletAsset[] = [];
    // Non-EVM (Tron): derive the address from the same seed, self-register,
    // and append TRX + TRC-20 balances as assets. Dormant-safe — yields
    // nothing when the backend chain is unconfigured. TRX shows as a native
    // token (visible at 0 in the zero-balance section) so Tron always appears.
    try {
      const tron = await loadTronWallet(addr || undefined);
      if (tron) {
        const tronChain = getChainDisplay('tron');
        let tId = 1;
        for (const row of tron.balances) {
          const bal = parseFloat(row.balance) || 0;
          const isNative = row.symbol === 'TRX';
          out.push({
            id: `tron-${tId++}`,
            symbol: row.symbol,
            name: isNative ? 'Tron' : row.name,
            balance: bal,
            usdValue: parseFloat(row.usdValue) || 0,
            usdPrice: row.usdPrice || 0,
            iconColor: tronChain?.iconColor || '#EF0027',
            // TRX gets the chain logo; TRC-20s (USDT) reuse the registry logo.
            iconUrl: isNative ? tronChain?.iconUrl : tokenList.find(t => t.symbol === row.symbol)?.iconUrl,
            contractAddress: row.contractAddress || undefined,
            decimals: row.decimals,
            details: {
              type: isNative ? 'Native Token' : 'Token',
              chain: 'Tron',
              chainKey: 'tron',
            },
          });
        }
      }
    } catch { /* chain unavailable — skip Tron */ }
    return out;
  }, [tokenList]);

  const loadBtcAssets = useCallback(async (addr: string): Promise<WalletAsset[]> => {
    const out: WalletAsset[] = [];
    // Non-EVM (BTC): same pattern — the BIP-84 address derives from the same
    // seed, self-registers, and BTC shows as a native token (visible at 0 in
    // the zero-balance section) so Bitcoin always appears.
    try {
      const btc = await loadBtcWallet(addr || undefined);
      if (btc) {
        const btcChain = getChainDisplay('btc');
        let bId = 1;
        for (const row of btc.balances) {
          out.push({
            id: `btc-${bId++}`,
            symbol: row.symbol,
            name: row.symbol === 'BTC' ? 'Bitcoin' : row.name,
            balance: parseFloat(row.balance) || 0,
            usdValue: parseFloat(row.usdValue) || 0,
            usdPrice: row.usdPrice || 0,
            iconColor: btcChain?.iconColor || '#F7931A',
            iconUrl: btcChain?.iconUrl,
            decimals: row.decimals,
            details: { type: 'Native Token', chain: 'Bitcoin', chainKey: 'btc' },
          });
        }
      }
    } catch { /* chain unavailable — skip BTC */ }
    return out;
  }, []);

  const loadSolanaAssets = useCallback(async (addr: string): Promise<WalletAsset[]> => {
    const out: WalletAsset[] = [];
    // Non-EVM (Solana): the Phantom-path address derives from the same seed,
    // self-registers, and SOL shows as a native token (visible at 0 in the
    // zero-balance section) so Solana always appears; held SPL tokens (auto-
    // discovered) ride alongside.
    try {
      const sol = await loadSolanaWallet(addr || undefined);
      if (sol) {
        const solChain = getChainDisplay('solana');
        let sId = 1;
        for (const row of sol.balances) {
          const isNative = row.symbol === 'SOL';
          out.push({
            id: `solana-${sId++}`,
            symbol: row.symbol,
            name: isNative ? 'Solana' : row.name,
            balance: parseFloat(row.balance) || 0,
            usdValue: parseFloat(row.usdValue) || 0,
            usdPrice: row.usdPrice || 0,
            iconColor: solChain?.iconColor || '#14F195',
            iconUrl: isNative ? solChain?.iconUrl : tokenList.find(t => t.symbol === row.symbol)?.iconUrl,
            contractAddress: row.contractAddress || undefined,
            decimals: row.decimals,
            details: {
              type: isNative ? 'Native Token' : 'Token',
              chain: 'Solana',
              chainKey: 'solana',
            },
          });
        }
      }
    } catch { /* chain unavailable — skip Solana */ }
    return out;
  }, [tokenList]);

  const loadSuiAssets = useCallback(async (addr: string): Promise<WalletAsset[]> => {
    const out: WalletAsset[] = [];
    // Non-EVM (Sui): same seed, self-registering, and SUI shows as a native
    // token (visible at 0 in the zero-balance section) so Sui always appears;
    // held coin types ride alongside. loadSuiWallet already swallows a dormant
    // backend and yields an address with no rows, so a chain that is switched
    // off costs the user an empty section rather than an error.
    try {
      const sui = await loadSuiWallet(addr || undefined);
      if (sui) {
        const suiChain = getChainDisplay('sui');
        let uId = 1;
        for (const row of sui.balances) {
          const isNative = row.symbol === 'SUI';
          out.push({
            id: `sui-${uId++}`,
            symbol: row.symbol,
            name: isNative ? 'Sui' : row.name,
            balance: parseFloat(row.balance) || 0,
            usdValue: parseFloat(row.usdValue) || 0,
            usdPrice: row.usdPrice || 0,
            iconColor: suiChain?.iconColor || '#4DA2FF',
            iconUrl: isNative ? suiChain?.iconUrl : tokenList.find(t => t.symbol === row.symbol)?.iconUrl,
            contractAddress: row.contractAddress || undefined,
            decimals: row.decimals,
            details: {
              type: isNative ? 'Native Token' : 'Token',
              chain: 'Sui',
              chainKey: 'sui',
            },
          });
        }
      }
    } catch { /* chain unavailable — skip Sui */ }
    return out;
  }, [tokenList]);

  const loadPremiumAssets = useCallback(async (): Promise<WalletAsset[]> => {
    const out: WalletAsset[] = [];
    // Spendable premium MCGP lives in Contract B (not as ERC-20 in the
    // wallet), so the standard balance read misses it. Show it as its OWN
    // tagged row — distinct from any ordinary (withdrawn) ERC-20 MCGP — so
    // the user can tell spendable from transferable.
    try {
      const { balance: premium, usdValue } = await getPremiumMcgpAsset();
      if (premium > 0) {
        const mcgpToken = tokenList.find(t => t.symbol === 'MCGP');
        out.push({
          id: 'mcgp-premium',
          symbol: 'MCGP',
          name: mcgpToken?.name || 'MCG Protocol',
          balance: premium,
          usdValue,
          usdPrice: premium > 0 ? usdValue / premium : 0,
          iconColor: mcgpToken?.iconColor || '#FFD700',
          iconUrl: mcgpToken?.iconUrl,
          tag: 'Spendable',
          details: { type: 'Token', chain: 'Sonic', chainKey: 'sonic' },
        });
      }
    } catch { /* MCGP swap unavailable — skip */ }
    return out;
  }, [tokenList]);

  // ── Orchestration ─────────────────────────────────────────────────────────
  //
  // Three phases, and the split is the whole point of this function:
  //
  //   0. Local reads (AsyncStorage). No network, so the shell — wallet label,
  //      address, actions, section headers — renders essentially immediately.
  //   1. The independent network sources, CONCURRENTLY. Each commits its rows
  //      the moment it returns, so the wait is the slowest chain rather than
  //      the sum of all of them.
  //   2. The merges that need the whole list (custom tokens, then discovery).
  //
  // Previously all of this ran in one sequential chain with a single setAssets
  // at the end, so the screen showed nothing at all until every chain had
  // answered — and one slow Esplora read held up balances that were ready.
  const fetchBalances = useCallback(async (refreshing = false) => {
    const fetchToken = ++fetchTokenRef.current;
    const isCurrent = () => fetchToken === fetchTokenRef.current;
    if (refreshing) setIsRefreshing(true);

    // ── Phase 0: local only ──
    await migrateFromSingleWallet();
    const list = await getWalletList();
    const addr = await getActiveWallet();
    if (!isCurrent()) return;

    setWalletList(list);
    if (addr) {
      setActiveAddress(addr);
      const meta = list.find(w => w.address === addr);
      setActiveLabel(meta?.label || 'Wallet');
    } else {
      // Must reset: without this, deleting the last wallet leaves a stale
      // address and the no-wallet onboarding branch becomes unreachable.
      setActiveAddress('');
      setActiveLabel('Wallet');
    }
    // The shell has everything it needs now; only the numbers are outstanding.
    setIsLoading(false);

    if (!addr) {
      loadedForRef.current = null;
      setSourceAssets(emptySourceAssets<WalletAsset>());
      setExtraAssets([]);
      setIsRefreshing(false);
      return;
    }

    // ── Phase 1: independent sources, in parallel ──
    //
    // Rows are only CLEARED when the wallet actually changed. A refetch for the
    // same wallet — which is every focus of this tab — revalidates in place, so
    // the list and total stay on screen instead of collapsing to a skeleton and
    // rebuilding each time the user navigates back.
    const isDifferentWallet = loadedForRef.current !== addr;
    loadedForRef.current = addr;
    if (isDifferentWallet) {
      setExtraAssets([]);
      // Seed the EVM slot with the native rows so every supported chain is
      // listed (at zero) from the first frame, then filled in as balances land.
      setSourceAssets({ ...emptySourceAssets<WalletAsset>(), evm: buildNativeAssets() });
    }

    // Collected locally as well as committed to state. Phase 2 needs the
    // complete list the instant the fan-out finishes, and React state is not
    // readable then — the updates are scheduled, not applied — so reading it
    // back would silently drop whichever source landed last.
    const collected = emptySourceAssets<WalletAsset>();

    const run = async (source: AssetSource, load: () => Promise<WalletAsset[]>) => {
      let rows: WalletAsset[] = [];
      try {
        rows = await load();
      } catch (err) {
        console.error(`Wallet: ${source} balances failed`, err);
      }
      collected[source] = rows;
      if (isCurrent()) setSourceAssets(prev => ({ ...prev, [source]: rows }));
    };

    await Promise.all([
      run('evm', loadEvmAssets),
      run('tron', () => loadTronAssets(addr)),
      run('btc', () => loadBtcAssets(addr)),
      run('solana', () => loadSolanaAssets(addr)),
      run('sui', () => loadSuiAssets(addr)),
      run('premium', loadPremiumAssets),
    ]);
    if (!isCurrent()) return;

    // ── Phase 2: merges that need the complete list ──
    // Deliberately after the fan-out: both dedupe against everything already
    // present, so running them per-source would re-add rows that had not
    // arrived yet. Each returns only its OWN rows, which are kept separate from
    // the sources so the next revalidation cannot be shadowed by a stale list.
    const base = combineSourceAssets(collected);
    try {
      const custom = await mergeCustomTokens(base, addr, customTokens);
      if (isCurrent()) setExtraAssets(custom);

      // Discovery is deliberately NOT awaited: it is the slowest merge and the
      // list is already complete without it, so blocking the refresh spinner on
      // it would make pull-to-refresh feel broken.
      void mergeDiscoveredTokens([...base, ...custom], addr).then((discovered) => {
        if (discovered.length && isCurrent()) setExtraAssets([...custom, ...discovered]);
      });
    } catch (err) {
      console.error('Wallet: token merge failed', err);
    } finally {
      if (isCurrent()) setIsRefreshing(false);
    }
    // `network` is read inside the loaders at call time (getNetwork()), not in
    // this closure, so the rule cannot see it. It must stay: useFocusEffect
    // keys off this callback's identity, and a mainnet/testnet switch has to
    // force a refetch.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    buildNativeAssets, customTokens, network,
    loadEvmAssets, loadTronAssets, loadBtcAssets, loadSolanaAssets, loadSuiAssets,
    loadPremiumAssets,
  ]);

  // Focus-driven, not mount-only. This screen is a TAB: creating a wallet ends
  // with router.replace('/tokenization'), which targets this already-mounted
  // screen, so it never remounts. With a plain mount effect activeAddress stays
  // '' and the user is bounced back to "Set Up Your Wallet" holding the wallet
  // they just made. (The old app/wallet/home.tsx got away with a mount effect
  // because it was a STACK route that the replace unmounted.)
  useFocusEffect(useCallback(() => { fetchBalances(); }, [fetchBalances]));

  const onRefresh = useCallback(() => fetchBalances(true), [fetchBalances]);

  const handleSwitchWallet = useCallback(async (address: string) => {
    setShowWalletSelector(false);
    if (address === activeAddress) return;
    setIsLoading(true);
    try {
      await setActiveWallet(address);
      await registerWalletAddress(address);
      await fetchBalances();
    } catch (err) {
      console.error('Switch wallet error:', err);
    }
  }, [activeAddress, fetchBalances]);

  const visibleAssets = useMemo(
    () => assets.filter(a => !hiddenTokens.has(tokenKey(a.details?.chainKey || '', a.symbol, a.contractAddress))),
    [assets, hiddenTokens]
  );

  const totalUsdValue = visibleAssets.reduce((s, a) => s + (Number.isFinite(a.usdValue) ? a.usdValue : 0), 0);

  const handleQuickAction = (action: string) => {
    if (action === 'fund') setShowFundModal(true);
    else if (action === 'send') setShowSendModal(true);
    else if (action === 'instant-pay') router.push('/wallet/instant-pay' as any);
    else if (action === 'swap') router.push('/easyswap' as any);
    else router.push(`/${action}` as any);
  };

  const handleAssetPress = (asset: WalletAsset) => {
    // Spendable premium MCGP is a Contract-B entitlement (buy/sell/pay), not a
    // transferable ERC-20 — route it to the MCGP hub. Ordinary (withdrawn)
    // MCGP falls through to the normal token/send flow.
    if (asset.symbol === 'MCGP' && asset.tag) {
      router.push('/wallet/mcgp' as any);
      return;
    }
    router.push({
      pathname: '/wallet/token',
      params: {
        symbol: asset.symbol,
        chainKey: asset.details?.chainKey || '',
        name: asset.name,
        iconColor: asset.iconColor,
        iconUrl: asset.iconUrl || '',
        type: asset.details?.type || '',
        // Discovered tokens aren't in supported_tokens, so the detail
        // screen must address them by contract. Pass balance/usdValue/
        // decimals as well so the detail and Send screens can render
        // and prepare a tx without re-hitting discovery (BE caches it
        // anyway, but skipping the round-trip keeps the UI snappy).
        contractAddress: asset.contractAddress || '',
        discovered: asset.discovered ? '1' : '',
        balance: asset.discovered ? String(asset.balance) : '',
        usdValue: asset.discovered ? String(asset.usdValue) : '',
        decimals: asset.discovered && typeof asset.decimals === 'number' ? String(asset.decimals) : '',
      },
    } as any);
  };

  const filtersActive = selectedChain !== 'all' || selectedCategory !== 'all';

  // Pill label, e.g. "All assets" / "SONIC · tStocks".
  const filterSummary = useMemo(() => {
    const chainLabel = selectedChain === 'all' ? null : getChainDisplay(selectedChain)?.shortName ?? selectedChain;
    const categoryLabel =
      selectedCategory === 'all'
        ? null
        : WALLET_CATEGORY_FILTERS.find(f => f.key === selectedCategory)?.label ?? selectedCategory;
    const parts = [chainLabel, categoryLabel].filter(Boolean);
    return parts.length ? parts.join(' · ') : 'All assets';
  }, [selectedChain, selectedCategory]);

  const chainFilteredAssets = useMemo(
    () => visibleAssets.filter(a => {
      if (selectedChain !== 'all' && a.details?.chainKey !== selectedChain) return false;
      if (selectedCategory !== 'all' && assetCategoryOf(a.symbol, categoryMap) !== selectedCategory) return false;
      return true;
    }),
    [visibleAssets, selectedChain, selectedCategory, categoryMap],
  );

  // MCGP always leads when held; other holdings by USD value; zero-balance
  // natives by the shared ranking (BTC, ETH, … — see tokenRankOf).
  const assetsWithBalance = chainFilteredAssets
    .filter(a => a.balance > 0 || a.details?.type === 'Imported')
    .sort(
      (a, b) =>
        (a.symbol === 'MCGP' ? 0 : 1) - (b.symbol === 'MCGP' ? 0 : 1) ||
        b.usdValue - a.usdValue ||
        tokenRankOf(a.symbol) - tokenRankOf(b.symbol),
    );
  const nativeZeroBalance = chainFilteredAssets
    .filter(a => a.balance === 0 && a.details?.type === 'Native Token')
    .sort((a, b) => tokenRankOf(a.symbol) - tokenRankOf(b.symbol) || a.symbol.localeCompare(b.symbol));

  const ASSET_PREVIEW = 5;
  const previewAssets = assetsWithBalance.slice(0, ASSET_PREVIEW);
  const previewNatives = nativeZeroBalance.slice(0, Math.max(0, ASSET_PREVIEW - previewAssets.length));
  const totalVisible = assetsWithBalance.length + nativeZeroBalance.length;
  const hasMoreTokens = totalVisible > ASSET_PREVIEW;

  // Skeleton loading
  if (isLoading && !isRefreshing) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <ScrollView contentContainerStyle={{ padding: spacing.lg }}>
          <Skeleton height={180} radius="lg" />
          <Skeleton height={300} radius="lg" style={{ marginTop: spacing.lg }} />
        </ScrollView>
      </SafeAreaView>
    );
  }

  // No wallet yet — the setup entry point ("Set Up Wallet" prompts from
  // checkout and the dashboard backup banner land here). Without this the
  // screen would render an empty balance sheet with no way to create one.
  if (!activeAddress) {
    return (
      <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
        <View style={{ flex: 1, justifyContent: 'center', alignItems: 'center', paddingHorizontal: spacing['3xl'] }}>
          <View
            style={{
              width: 88,
              height: 88,
              borderRadius: radius.pill,
              backgroundColor: colors.accentMuted,
              justifyContent: 'center',
              alignItems: 'center',
              marginBottom: spacing.xl,
            }}
          >
            <Icon name="wallet" size={48} color={colors.accent} />
          </View>
          <Text variant="title2" weight="700" align="center" style={{ marginBottom: spacing.sm }}>
            Set Up Your Wallet
          </Text>
          <Text variant="bodySm" color="textSecondary" align="center" style={{ marginBottom: spacing['2xl'] }}>
            A wallet is needed to make purchases, receive cashback, and manage your funds securely.
          </Text>
          <Button
            title="Create New Wallet"
            leftIcon="plus"
            onPress={() => router.push('/wallet/seedphrase')}
            style={{ marginBottom: spacing.md }}
          />
          <Button
            title="Import Existing Wallet"
            variant="secondary"
            leftIcon="upload"
            onPress={() => router.push('/wallet/seedphrase')}
          />
        </View>
      </SafeAreaView>
    );
  }

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] * 2.5 }}
        showsVerticalScrollIndicator={false}
        refreshControl={
          <RefreshControl refreshing={isRefreshing} onRefresh={onRefresh} tintColor={colors.accent} colors={[colors.accent]} />
        }
      >
        <PressableScale
          onPress={() => switchNetwork(network === 'mainnet' ? 'testnet' : 'mainnet')}
          style={{
            flexDirection: 'row',
            alignItems: 'center',
            alignSelf: 'center',
            gap: spacing.xs,
            backgroundColor: network === 'mainnet' ? colors.successBg : colors.warningBg,
            paddingHorizontal: spacing.md,
            paddingVertical: spacing.xs,
            borderRadius: radius.pill,
            marginBottom: spacing.md,
          }}
        >
          <View
            style={{
              width: 8,
              height: 8,
              borderRadius: 4,
              backgroundColor: network === 'mainnet' ? colors.success : colors.warning,
            }}
          />
          <Text variant="caption" weight="600">
            {network === 'mainnet' ? 'Mainnet' : 'Testnet'}
          </Text>
        </PressableScale>

        <WalletBalanceCard
          totalUsdValue={totalUsdValue}
          isTotalPending={isTotalPending}
          isValuesHidden={isValuesHidden}
          activeLabel={activeLabel}
          onToggleVisibility={() => setIsValuesHidden(v => !v)}
          onQuickAction={handleQuickAction}
          onWalletPress={() => setShowWalletSelector(true)}
        />

        {/* Assets */}
        <AccentCard padding="lg" railColor="transparent" style={{ marginTop: spacing.lg }}>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.md }}>
            <Text variant="title3" weight="700">
              Assets
            </Text>
            <View style={{ flexDirection: 'row', gap: spacing.md }}>
              <PressableScale
                style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}
                onPress={() => router.push('/activity' as any)}
              >
                <Icon name="clock" size={15} color={colors.textMuted} />
                <Text variant="caption" color="textMuted" weight="600">
                  Activity
                </Text>
              </PressableScale>
              <PressableScale
                style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}
                onPress={() => setShowManageModal(true)}
              >
                <Icon name="filter" size={15} color={colors.textMuted} />
                <Text variant="caption" color="textMuted" weight="600">
                  Manage
                </Text>
              </PressableScale>
            </View>
          </View>

          {/* Chain + category filters live in one sheet — two stacked chip rows
              ate the vertical space above the asset list. */}
          <PressableScale
            onPress={() => setShowFilterSheet(true)}
            accessibilityRole="button"
            accessibilityLabel={`Filter assets. Current filter: ${filterSummary}`}
            style={{
              flexDirection: 'row',
              alignItems: 'center',
              gap: spacing.xs,
              alignSelf: 'flex-start',
              paddingVertical: spacing.xs,
              paddingHorizontal: spacing.md,
              marginBottom: spacing.sm,
              borderRadius: radius.pill,
              borderWidth: 1,
              borderColor: filtersActive ? colors.accent : colors.border,
              backgroundColor: filtersActive ? colors.accentMuted : colors.surface,
            }}
          >
            <Icon name="filter" size={14} color={filtersActive ? colors.accent : colors.textSecondary} />
            <Text
              variant="bodySm"
              weight={filtersActive ? '600' : '400'}
              style={{ color: filtersActive ? colors.accent : colors.textSecondary }}
            >
              {filterSummary}
            </Text>
            <Icon name="chevronDown" size={14} color={filtersActive ? colors.accent : colors.textMuted} />
          </PressableScale>

          {previewAssets.map(asset => (
            <AssetRow key={asset.id} asset={asset} isValuesHidden={isValuesHidden} onPress={() => handleAssetPress(asset)} />
          ))}

          {previewNatives.length > 0 && (
            <>
              {previewAssets.length > 0 && <Divider spacing="sm" />}
              <Text variant="overline" color="textMuted" style={{ marginTop: spacing.sm, marginBottom: spacing.xs }}>
                NETWORKS
              </Text>
              {previewNatives.map(asset => (
                <AssetRow key={asset.id} asset={asset} isValuesHidden={isValuesHidden} onPress={() => handleAssetPress(asset)} />
              ))}
            </>
          )}

          {assetsWithBalance.length === 0 && nativeZeroBalance.length === 0 && (
            <EmptyState
              icon="wallet"
              title="No tokens found"
              subtitle="Pull to refresh or fund your wallet"
            />
          )}

          {hasMoreTokens && (
            <>
              <Divider spacing="sm" />
              <PressableScale
                style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'center', gap: spacing.xs, paddingVertical: spacing.md }}
                onPress={() => router.push('/wallet/tokens' as any)}
              >
                <Text variant="bodySm" weight="600" color="accent">
                  See all {totalVisible} tokens
                </Text>
                <Icon name="chevronRight" size={16} color={colors.accent} />
              </PressableScale>
            </>
          )}
        </AccentCard>

        {/* Recent Activity */}
        <AccentCard padding="lg" railColor="transparent" style={{ marginTop: spacing.lg }}>
          <View style={{ flexDirection: 'row', justifyContent: 'space-between', alignItems: 'center', marginBottom: spacing.md }}>
            <Text variant="title3" weight="700">
              Recent Activity
            </Text>
            <PressableScale
              style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}
              onPress={() => router.push('/activity' as any)}
            >
              <Text variant="caption" color="textMuted" weight="600">
                See all
              </Text>
              <Icon name="chevronRight" size={15} color={colors.textMuted} />
            </PressableScale>
          </View>
          <ActivityFeed
            types={['wallet', 'swap']}
            embedded
            onSeeAll={() => router.push('/wallet/history' as any)}
          />
        </AccentCard>

        <FundModal visible={showFundModal} onClose={() => setShowFundModal(false)} />
        <SendModal visible={showSendModal} onClose={() => setShowSendModal(false)} />
        <ManageTokensModal
          visible={showManageModal}
          assets={assets}
          hiddenTokens={hiddenTokens}
          tokenKey={tokenKey}
          onToggle={toggleHidden}
          onClose={() => setShowManageModal(false)}
          onImport={() => {
            setShowManageModal(false);
            router.push('/wallet/import-token' as any);
          }}
        />

        {/* Wallet Selector */}
        <Sheet visible={showWalletSelector} onClose={() => setShowWalletSelector(false)} title="Select Wallet">
          <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.lg }}>
            {walletList.map((w) => (
              <SheetOption
                key={w.address}
                icon="wallet"
                title={w.label}
                desc={`${w.address.slice(0, 8)}...${w.address.slice(-6)}`}
                iconColor={w.address === activeAddress ? colors.accent : colors.textMuted}
                iconBg={w.address === activeAddress ? colors.accentMuted : colors.surfaceAlt}
                trailing={
                  w.address === activeAddress ? (
                    <Icon name="checkCircle" size={22} color={colors.accent} />
                  ) : (
                    <View />
                  )
                }
                onPress={() => handleSwitchWallet(w.address)}
              />
            ))}
            <PressableScale
              style={{ alignItems: 'center', paddingVertical: spacing.md, marginTop: spacing.xs }}
              onPress={() => { setShowWalletSelector(false); router.push('/wallet/manage'); }}
            >
              <Text variant="bodyMed" weight="600" color="accent">
                Manage Wallets
              </Text>
            </PressableScale>
          </View>
        </Sheet>

        <Sheet
          visible={showFilterSheet}
          onClose={() => setShowFilterSheet(false)}
          title="Filter assets"
          scrollable
        >
          <View style={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.lg, gap: spacing.lg }}>
            <View>
              <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
                CHAIN
              </Text>
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs }}>
                <ManageChip active={selectedChain === 'all'} label="All" onPress={() => setSelectedChain('all')} />
                {(Object.entries(CHAINS) as [ChainKey, (typeof CHAINS)[ChainKey]][]).map(([key, chain]) => {
                  const isActive = selectedChain === key;
                  const hasBalance = visibleAssets.some(a => a.details?.chainKey === key && a.balance > 0);
                  return (
                    <View key={key} style={{ flexDirection: 'row', alignItems: 'center' }}>
                      <ManageChip
                        active={isActive}
                        label={chain.shortName}
                        iconUrl={chain.iconUrl}
                        iconColor={chain.iconColor}
                        onPress={() => setSelectedChain(key)}
                      />
                      {hasBalance && !isActive && (
                        <View style={{ width: 5, height: 5, borderRadius: 2.5, backgroundColor: colors.success, marginLeft: 2 }} />
                      )}
                    </View>
                  );
                })}
                {NONEVM_CHAIN_KEYS.map((key) => {
                  const chain = getChainDisplay(key);
                  if (!chain) return null;
                  const isActive = selectedChain === key;
                  const hasBalance = visibleAssets.some(a => a.details?.chainKey === key && a.balance > 0);
                  return (
                    <View key={key} style={{ flexDirection: 'row', alignItems: 'center' }}>
                      <ManageChip
                        active={isActive}
                        label={chain.shortName}
                        iconColor={chain.iconColor}
                        onPress={() => setSelectedChain(key)}
                      />
                      {hasBalance && !isActive && (
                        <View style={{ width: 5, height: 5, borderRadius: 2.5, backgroundColor: colors.success, marginLeft: 2 }} />
                      )}
                    </View>
                  );
                })}
              </View>
            </View>

            <View>
              <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
                CATEGORY
              </Text>
              {!!listingsError && (
                <Text variant="caption" color="danger" style={{ marginBottom: spacing.sm }}>
                  Couldn’t load asset categories, so this filter may be incomplete.
                </Text>
              )}
              <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.xs }}>
                {WALLET_CATEGORY_FILTERS.map((f) => (
                  <ManageChip
                    key={f.key}
                    active={selectedCategory === f.key}
                    label={f.label}
                    onPress={() => setSelectedCategory(f.key)}
                  />
                ))}
              </View>
            </View>

            {filtersActive && (
              <PressableScale
                onPress={() => {
                  setSelectedChain('all');
                  setSelectedCategory('all');
                }}
                style={{ alignItems: 'center', paddingVertical: spacing.sm }}
              >
                <Text variant="bodyMed" weight="600" color="accent">
                  Clear filters
                </Text>
              </PressableScale>
            )}
          </View>
        </Sheet>
      </ScrollView>
    </SafeAreaView>
  );
};

export default WalletScreen;
