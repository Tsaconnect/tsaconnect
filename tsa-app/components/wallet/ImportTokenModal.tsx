// components/wallet/ImportTokenModal.tsx
//
// Import-token modal — a 3-step flow (enter contract address → read on-chain
// token data → confirm + import) launched from the wallet home "Import Token"
// action. Restyled onto the TSA Signature design system (theme tokens +
// @/components/ui primitives), now built on the shared Sheet primitive.
//
// FINANCIAL component: the props interface + default export, ERC20 address
// validation, on-chain token lookup (readTokenContract), the duplicate-symbol
// guard, and the importToken persistence call are all preserved verbatim — this
// is a visual/structural restyle only. The hashColor() helper is registry-style
// data (the custom token's icon color), not chrome, so it is kept unchanged.
//
// Screen-fit layout: content scrolls in the Sheet body; the step's primary
// CTA (Look Up / Import / Back) is pinned in the Sheet footer.
import React, { useState } from 'react';
import { View, Alert } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { readTokenContract } from '../../services/wallet';
import { useTokens, type CustomToken } from '../../hooks/useTokens';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  Icon,
  IconButton,
  Input,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
  Sheet,
} from '@/components/ui';
import ChainSelector from './ChainSelector';

interface ImportTokenModalProps {
  visible: boolean;
  onClose: () => void;
}

type Step = 'input' | 'loading' | 'confirm';

function hashColor(str: string): string {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  const hue = Math.abs(hash) % 360;
  return `hsl(${hue}, 55%, 50%)`;
}

const ImportTokenModal = ({ visible, onClose }: ImportTokenModalProps) => {
  const { colors, spacing, radius } = useTheme();
  const { tokens, importToken } = useTokens();
  const [selectedChain, setSelectedChain] = useState<ChainKey>('sonic');
  const [contractAddress, setContractAddress] = useState('');
  const [step, setStep] = useState<Step>('input');
  const [error, setError] = useState('');
  const [tokenData, setTokenData] = useState<{ name: string; symbol: string; decimals: number } | null>(null);

  const reset = () => {
    setContractAddress('');
    setStep('input');
    setError('');
    setTokenData(null);
    setSelectedChain('sonic');
  };

  const handleClose = () => {
    reset();
    onClose();
  };

  const handlePaste = async () => {
    const text = await Clipboard.getStringAsync();
    if (text) {
      setContractAddress(text.trim());
      setError('');
    }
  };

  const handleLookup = async () => {
    setError('');
    if (!contractAddress.trim() || !contractAddress.startsWith('0x') || contractAddress.length !== 42) {
      setError('Enter a valid contract address (0x...)');
      return;
    }

    setStep('loading');
    const result = await readTokenContract(contractAddress.trim(), selectedChain);
    if (!result) {
      setError('Could not read token data from this address. Make sure it is a valid ERC20 contract.');
      setStep('input');
      return;
    }

    if (tokens[result.symbol]) {
      setError(`${result.symbol} is already a supported token`);
      setStep('input');
      return;
    }

    setTokenData(result);
    setStep('confirm');
  };

  const handleImport = async () => {
    if (!tokenData) return;
    try {
      const customToken: CustomToken = {
        symbol: tokenData.symbol,
        name: tokenData.name,
        decimals: tokenData.decimals,
        iconColor: hashColor(tokenData.symbol),
        chains: [selectedChain],
        contractAddress: contractAddress.trim(),
        chainKey: selectedChain,
        custom: true,
        importedAt: new Date().toISOString(),
      };
      await importToken(customToken);
      Alert.alert('Token Imported', `${tokenData.symbol} has been added to your wallet.`);
      handleClose();
    } catch (err: any) {
      setError(err.message || 'Failed to import token');
      setStep('input');
    }
  };

  const errorBanner = error ? (
    <AccentCard padding="md" railColor={colors.danger} style={{ marginTop: spacing.md }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
        <Icon name="warning" size={16} color={colors.danger} />
        <Text variant="caption" color="danger" style={{ flex: 1 }}>
          {error}
        </Text>
      </View>
    </AccentCard>
  ) : null;

  // Pinned footer CTA per step (loading has none).
  const footer =
    step === 'input' ? (
      <Button title="Look Up Token" onPress={handleLookup} disabled={!contractAddress.trim()} />
    ) : step === 'confirm' && tokenData ? (
      <>
        <Button title={`Import ${tokenData.symbol}`} onPress={handleImport} />
        <Button title="Back" variant="ghost" onPress={() => setStep('input')} />
      </>
    ) : null;

  return (
    <Sheet visible={visible} onClose={handleClose} title="Import Token" scrollable footer={footer}>
      <View style={{ padding: spacing.lg, paddingTop: spacing.md }}>
        <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
          NETWORK
        </Text>
        <ChainSelector
          availableChains={CHAIN_KEYS}
          selectedChain={selectedChain}
          onSelect={(chain) => { setSelectedChain(chain as ChainKey); setError(''); }}
          size="normal"
        />

        {step === 'input' && (
          <>
            <Text variant="overline" color="textMuted" style={{ marginTop: spacing.xl, marginBottom: spacing.sm }}>
              CONTRACT ADDRESS
            </Text>
            <Input
              placeholder="0x..."
              value={contractAddress}
              onChangeText={(t) => { setContractAddress(t); setError(''); }}
              autoCapitalize="none"
              autoCorrect={false}
              rightAdornment={
                <IconButton
                  icon="copy"
                  accessibilityLabel="Paste from clipboard"
                  onPress={handlePaste}
                  size={18}
                  variant="surface"
                  color={colors.accent}
                />
              }
            />

            {errorBanner}
          </>
        )}

        {step === 'loading' && (
          <View style={{ alignItems: 'center', paddingVertical: spacing['4xl'] }}>
            <Spinner size="large" label={`Reading contract on ${CHAINS[selectedChain].name}...`} />
          </View>
        )}

        {step === 'confirm' && tokenData && (
          <>
            <View
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                backgroundColor: colors.surfaceAlt,
                borderRadius: radius.lg,
                padding: spacing.lg,
                marginTop: spacing.xl,
              }}
            >
              <View
                style={{
                  width: 44,
                  height: 44,
                  borderRadius: radius.pill,
                  justifyContent: 'center',
                  alignItems: 'center',
                  backgroundColor: hashColor(tokenData.symbol),
                }}
              >
                <Text variant="title3" weight="700" style={{ color: colors.onAccent }}>
                  {tokenData.symbol[0]}
                </Text>
              </View>
              <View style={{ flex: 1, marginLeft: spacing.md }}>
                <Text variant="bodyMed" weight="700">
                  {tokenData.name}
                </Text>
                <Text variant="bodySm" color="textMuted">
                  {tokenData.symbol}
                </Text>
              </View>
            </View>

            <View
              style={{
                backgroundColor: colors.surfaceAlt,
                borderRadius: radius.md,
                paddingHorizontal: spacing.lg,
                paddingVertical: spacing.xs,
                marginTop: spacing.md,
              }}
            >
              <SummaryCard>
                <SummaryRow label="Symbol" value={tokenData.symbol} />
                <SummaryRow label="Decimals" value={String(tokenData.decimals)} />
                <SummaryRow label="Network" value={CHAINS[selectedChain].name} />
                <SummaryRow
                  label="Contract"
                  value={
                    <Text
                      variant="bodyMed"
                      weight="600"
                      numeric
                      numberOfLines={1}
                      ellipsizeMode="middle"
                      style={{ flexShrink: 1, maxWidth: '60%' }}
                    >
                      {contractAddress}
                    </Text>
                  }
                />
              </SummaryCard>
            </View>

            {errorBanner}
          </>
        )}
      </View>
    </Sheet>
  );
};

export default ImportTokenModal;
