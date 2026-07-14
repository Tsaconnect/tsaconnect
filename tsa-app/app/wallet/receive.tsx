// app/wallet/receive.tsx
//
// Receive — shows the active wallet address as a scannable QR code plus a
// copyable/shareable address, with a chain selector for the receiving network.
// Restyled onto the TSA Signature design system (theme tokens + @/components/ui
// primitives).
//
// FINANCIAL screen: the address derivation (getActiveWallet), the QR encoding of
// that exact address, the chain selection (ChainSelector + CHAINS/CHAIN_KEYS), and
// the copy/share handlers are preserved verbatim — this is a visual/structural
// restyle only. The QR is intentionally rendered dark-on-light at a fixed size so
// it stays scannable in both light and dark themes.
import React, { useState, useEffect } from 'react';
import { View, Share } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { ScrollView } from 'react-native';
import { getActiveWallet } from '@/services/wallet';
import * as Clipboard from 'expo-clipboard';
import QRCode from 'react-native-qrcode-svg';
import {
  CHAIN_KEYS,
  NONEVM_CHAIN_KEYS,
  getChainDisplay,
  isNonEVMChain,
  getSupportedNetworkNames,
  type AnyChainKey,
} from '../../constants/chains';
import { getTronAddress } from '../../services/chains/tronWallet';
import { getBtcAddress } from '../../services/chains/btcWallet';
import { getSolanaAddress } from '../../services/chains/solanaWallet';
import { getSuiAddress } from '../../services/chains/suiWallet';
import ChainSelector from '../../components/wallet/ChainSelector';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  Card,
  EmptyState,
  Icon,
  Text,
} from '@/components/ui';

// QR codes must stay high-contrast dark-on-light to remain scannable regardless of
// the active theme, so these two values are intentionally not theme-driven.
const QR_FG = '#16201F';
const QR_BG = '#FFFFFF';

const RECEIVE_CHAINS: AnyChainKey[] = [...CHAIN_KEYS, ...NONEVM_CHAIN_KEYS];

const ReceiveToken = () => {
  const { colors, spacing, radius } = useTheme();
  const [evmAddress, setEvmAddress] = useState('');
  const [tronAddress, setTronAddress] = useState('');
  const [btcAddress, setBtcAddress] = useState('');
  const [solanaAddress, setSolanaAddress] = useState('');
  const [suiAddress, setSuiAddress] = useState('');
  const [copied, setCopied] = useState(false);
  const [selectedChain, setSelectedChain] = useState<AnyChainKey>('sonic');

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

  // The address to show/QR/copy depends on the selected chain's family: EVM
  // chains share one 0x address; Tron, BTC, Solana, and Sui each derive their own.
  const nonEvmAddress =
    selectedChain === 'btc'
      ? btcAddress
      : selectedChain === 'solana'
        ? solanaAddress
        : selectedChain === 'sui'
          ? suiAddress
          : tronAddress;
  const walletAddress = isNonEVMChain(selectedChain) ? nonEvmAddress : evmAddress;
  const selectedChainName = getChainDisplay(selectedChain)?.name || 'Unknown';

  const handleCopy = async () => {
    if (!walletAddress) return;
    await Clipboard.setStringAsync(walletAddress);
    setCopied(true);
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
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing['2xl'] }}>
          Share your address or QR code to receive tokens on supported networks ({getSupportedNetworkNames()}).
        </Text>

        <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
          NETWORK
        </Text>
        <ChainSelector
          availableChains={RECEIVE_CHAINS}
          selectedChain={selectedChain}
          onSelect={setSelectedChain}
        />
        <Text
          variant="caption"
          color="textMuted"
          align="center"
          style={{ marginTop: spacing.md, marginBottom: spacing.sm }}
        >
          Send tokens on {selectedChainName} to this address
        </Text>

        <View style={{ alignItems: 'center', marginVertical: spacing['2xl'] }}>
          {walletAddress ? (
            <Card
              padding="2xl"
              style={{ backgroundColor: QR_BG, borderRadius: radius.lg, alignItems: 'center' }}
            >
              <QRCode
                value={walletAddress}
                size={200}
                color={QR_FG}
                backgroundColor={QR_BG}
              />
            </Card>
          ) : (
            <EmptyState
              icon="wallet"
              title="No wallet address"
              subtitle="Set up a wallet to receive tokens"
            />
          )}
        </View>

        <Card padding="lg" style={{ alignItems: 'center', marginBottom: spacing.xl }}>
          <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.xs }}>
            Your Wallet Address
          </Text>
          <Text variant="bodySm" weight="500" align="center" numeric selectable numberOfLines={2}>
            {walletAddress || 'Not available'}
          </Text>
        </Card>

        <View style={{ flexDirection: 'row', gap: spacing.md, marginBottom: spacing.xl }}>
          <Button
            title={copied ? 'Copied!' : 'Copy Address'}
            leftIcon={copied ? 'check' : 'copy'}
            onPress={handleCopy}
            style={{ flex: 1 }}
          />
          <Button
            title="Share"
            variant="secondary"
            leftIcon="send"
            onPress={handleShare}
            style={{ flex: 1 }}
          />
        </View>

        <AccentCard padding="md" railColor={colors.warning}>
          <View style={{ flexDirection: 'row', gap: spacing.sm }}>
            <Icon name="warning" size={18} color={colors.warning} />
            <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
              Only send tokens on the {selectedChainName} network to this address.
              Sending from a different network may result in permanent loss.
            </Text>
          </View>
        </AccentCard>
      </ScrollView>
    </SafeAreaView>
  );
};

export default ReceiveToken;
