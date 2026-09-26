// app/wallet/receive.tsx
//
// Receive — shows the active wallet address as a scannable QR code plus a
// copyable/shareable address, with a network dropdown for the receiving chain.
// Opened from a token's detail screen it arrives with that token and chain
// (params `symbol`, `chainKey`) already selected.
//
// FINANCIAL screen: the address derivation (getActiveWallet + per-family
// derivations), the QR encoding of that exact address, and the copy/share
// handlers are unchanged. The QR is intentionally rendered dark-on-light at a
// fixed size so it stays scannable in both light and dark themes.
import React, { useState, useEffect, useMemo } from 'react';
import { View, Share, ScrollView } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Stack, useLocalSearchParams } from 'expo-router';
import { getActiveWallet } from '@/services/wallet';
import * as Clipboard from 'expo-clipboard';
import QRCode from 'react-native-qrcode-svg';
import {
  CHAIN_KEYS,
  NONEVM_CHAIN_KEYS,
  getChainDisplay,
  isNonEVMChain,
  type AnyChainKey,
} from '../../constants/chains';
import { getTronAddress } from '../../services/chains/tronWallet';
import { getBtcAddress } from '../../services/chains/btcWallet';
import { getSolanaAddress } from '../../services/chains/solanaWallet';
import { getSuiAddress } from '../../services/chains/suiWallet';
import TokenAvatar from '../../components/wallet/TokenAvatar';
import { useTokens } from '../../hooks/useTokens';
import { useTheme } from '@/theme';
import {
  AccentCard,
  BottomActionBar,
  Button,
  Card,
  EmptyState,
  Icon,
  PickerSheet,
  PressableScale,
  SelectRow,
  Text,
  useToast,
  type PickerItem,
} from '@/components/ui';

// QR codes must stay high-contrast dark-on-light to remain scannable regardless of
// the active theme, so these two values are intentionally not theme-driven.
const QR_FG = '#16201F';
const QR_BG = '#FFFFFF';

const RECEIVE_CHAINS: AnyChainKey[] = [...CHAIN_KEYS, ...NONEVM_CHAIN_KEYS];
const QR_SIZE = 200;
const QR_LOGO_SIZE = 44;

const shortAddress = (addr: string) => (addr ? `${addr.slice(0, 8)}…${addr.slice(-6)}` : 'Not available');

const ReceiveToken = () => {
  const { colors, spacing, radius } = useTheme();
  const toast = useToast();
  const { tokens } = useTokens();
  const params = useLocalSearchParams<{ chainKey?: string; symbol?: string }>();
  const [evmAddress, setEvmAddress] = useState('');
  const [tronAddress, setTronAddress] = useState('');
  const [btcAddress, setBtcAddress] = useState('');
  const [solanaAddress, setSolanaAddress] = useState('');
  const [suiAddress, setSuiAddress] = useState('');
  const [copied, setCopied] = useState(false);
  const [pickerOpen, setPickerOpen] = useState(false);
  const openedOnChain = RECEIVE_CHAINS.includes(params.chainKey as AnyChainKey)
    ? (params.chainKey as AnyChainKey)
    : undefined;
  const [selectedChain, setSelectedChain] = useState<AnyChainKey>(openedOnChain ?? 'sonic');

  useEffect(() => {
    const loadAddresses = async () => {
      const [evm, tron, btc, sol, sui] = await Promise.all([
        getActiveWallet(),
        // Same mnemonic, standard Tron path — reproduces the TronLink address.
        getTronAddress().catch(() => null),
        // Same mnemonic, BIP-84 path — reproduces the Sparrow/Electrum address.
        getBtcAddress().catch(() => null),
        // Same mnemonic, Phantom path — reproduces the Phantom address.
        getSolanaAddress().catch(() => null),
        // Same mnemonic, Sui path (m/44'/784') — reproduces the Sui wallet address.
        getSuiAddress().catch(() => null),
      ]);
      if (evm) setEvmAddress(evm);
      if (tron) setTronAddress(tron);
      if (btc) setBtcAddress(btc);
      if (sol) setSolanaAddress(sol);
      if (sui) setSuiAddress(sui);
    };
    loadAddresses();
  }, []);

  // The address to show/QR/copy depends on the chain's family: EVM chains share
  // one 0x address; Tron, BTC, Solana, and Sui each derive their own.
  const addressFor = (chain: AnyChainKey) => {
    if (!isNonEVMChain(chain)) return evmAddress;
    return chain === 'btc'
      ? btcAddress
      : chain === 'solana'
        ? solanaAddress
        : chain === 'sui'
          ? suiAddress
          : tronAddress;
  };
  const walletAddress = addressFor(selectedChain);
  const chain = getChainDisplay(selectedChain);
  const selectedChainName = chain?.name || 'Unknown';
  const isEvm = !isNonEVMChain(selectedChain);
  // Name the token only on a network that carries it: after switching USDC→Bitcoin
  // the screen must not claim "Your USDC address on Bitcoin".
  const symbol =
    params.symbol &&
    (selectedChain === openedOnChain || (tokens[params.symbol]?.chains as AnyChainKey[] | undefined)?.includes(selectedChain))
      ? params.symbol
      : '';

  const chainAvatar = (key: AnyChainKey, size: number) => {
    const c = getChainDisplay(key);
    return (
      <TokenAvatar
        symbol={c?.nativeCurrency.symbol || c?.shortName || ''}
        iconUrl={c?.iconUrl}
        iconColor={c?.iconColor}
        size={size}
      />
    );
  };

  const networkItems = useMemo<PickerItem[]>(
    () =>
      RECEIVE_CHAINS.map((key) => {
        const c = getChainDisplay(key);
        const evm = !isNonEVMChain(key);
        return {
          id: key,
          label: c?.name || key,
          sublabel: shortAddress(addressFor(key)),
          group: evm ? 'EVM networks · same address' : 'Other networks',
          leftElement: chainAvatar(key, 32),
        };
      }),
    // addressFor reads the address state below; chainAvatar is pure.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [evmAddress, tronAddress, btcAddress, solanaAddress, suiAddress],
  );

  const handleCopy = async () => {
    if (!walletAddress) return;
    await Clipboard.setStringAsync(walletAddress);
    setCopied(true);
    toast.success('Address copied');
    setTimeout(() => setCopied(false), 2000);
  };

  const handleShare = async () => {
    if (!walletAddress) return;
    try {
      await Share.share({
        message: `My TSA Connect wallet address: ${walletAddress}`,
      });
    } catch (err) {
      console.error('Share error:', err);
    }
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Stack.Screen options={{ title: symbol ? `Receive ${symbol}` : 'Receive' }} />
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['2xl'], gap: spacing.lg }}>
        <SelectRow
          label="Network"
          value={selectedChainName}
          leftElement={chainAvatar(selectedChain, 28)}
          onPress={() => setPickerOpen(true)}
        />

        {walletAddress ? (
          <Card padding="xl" style={{ alignItems: 'center' }}>
            <View
              style={{
                padding: spacing.sm,
                backgroundColor: QR_BG,
                borderRadius: radius.lg,
                borderWidth: 1,
                borderColor: colors.hairline,
              }}
            >
              <QRCode value={walletAddress} size={QR_SIZE} color={QR_FG} backgroundColor={QR_BG} ecl="H" />
              {/* ecl H tolerates the logo covering the centre modules */}
              <View
                pointerEvents="none"
                style={{
                  position: 'absolute',
                  top: 0,
                  bottom: 0,
                  left: 0,
                  right: 0,
                  alignItems: 'center',
                  justifyContent: 'center',
                }}
              >
                <View style={{ padding: 3, borderRadius: radius.pill, backgroundColor: QR_BG }}>
                  {symbol ? (
                    <TokenAvatar symbol={symbol} chainKey={selectedChain} preferRegistry size={QR_LOGO_SIZE} />
                  ) : (
                    chainAvatar(selectedChain, QR_LOGO_SIZE)
                  )}
                </View>
              </View>
            </View>

            <Text variant="bodySm" color="textSecondary" align="center" style={{ marginTop: spacing.lg }}>
              Your {symbol ? <Text variant="bodySm" weight="600">{symbol} </Text> : null}address on{' '}
              <Text variant="bodySm" weight="600">{selectedChainName}</Text>
            </Text>

            <PressableScale
              onPress={handleCopy}
              accessibilityRole="button"
              accessibilityLabel="Copy address"
              style={{
                marginTop: spacing.md,
                alignSelf: 'stretch',
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.md,
                padding: spacing.md,
                borderRadius: radius.md,
                backgroundColor: colors.surfaceAlt,
              }}
            >
              <Text variant="bodySm" color="textSecondary" numeric selectable style={{ flex: 1 }}>
                <Text variant="bodySm" weight="700">{walletAddress.slice(0, 6)}</Text>
                {walletAddress.slice(6, -6)}
                <Text variant="bodySm" weight="700">{walletAddress.slice(-6)}</Text>
              </Text>
              <Icon name={copied ? 'check' : 'copy'} size={18} color={colors.accent} />
            </PressableScale>
          </Card>
        ) : (
          <EmptyState icon="wallet" title="No wallet address" subtitle="Set up a wallet to receive tokens" />
        )}

        <AccentCard padding="md" railColor={colors.warning}>
          <View style={{ flexDirection: 'row', gap: spacing.sm }}>
            <Icon name="warning" size={18} color={colors.warning} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              Only send {symbol || 'assets'} on the {selectedChainName} network to this address.{' '}
              {isEvm
                ? 'The same address works on every EVM network in TSA Connect, but funds sent on a network TSA Connect does not support will not show in your wallet.'
                : 'Sending from a different network may result in permanent loss.'}
            </Text>
          </View>
        </AccentCard>
      </ScrollView>

      <BottomActionBar>
        <View style={{ flexDirection: 'row', gap: spacing.md }}>
          <Button
            title={copied ? 'Copied!' : 'Copy address'}
            leftIcon={copied ? 'check' : 'copy'}
            onPress={handleCopy}
            disabled={!walletAddress}
            style={{ flex: 2 }}
          />
          <Button
            title="Share"
            variant="secondary"
            leftIcon="share"
            onPress={handleShare}
            disabled={!walletAddress}
            style={{ flex: 1 }}
          />
        </View>
      </BottomActionBar>

      <PickerSheet
        visible={pickerOpen}
        onClose={() => setPickerOpen(false)}
        title="Select network"
        items={networkItems}
        selectedId={selectedChain}
        onSelect={(item) => {
          setSelectedChain(item.id as AnyChainKey);
          setPickerOpen(false);
        }}
      />
    </SafeAreaView>
  );
};

export default ReceiveToken;
