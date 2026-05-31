// app/wallet/import-token.tsx
//
// "Import crypto" screen — full-page variant of the existing
// components/wallet/ImportTokenModal flow. Auto-fills name/symbol/decimals
// by reading the ERC-20 contract on the selected chain, then persists the
// custom token via useTokens().importToken (AsyncStorage, per-network).
//
// Migrated onto the TSA Signature design system (@/theme tokens +
// @/components/ui primitives). FINANCIAL screen: address validation, the
// ERC-20 contract lookup (readTokenContract), decimals bounds-checking, and
// the add-token persistence (importToken) are preserved verbatim — this is a
// visual/structural restyle only. Chain icon Images keep their remote URLs.

import React, { useCallback, useMemo, useState } from 'react';
import {
  View,
  ScrollView,
  Alert,
  KeyboardAvoidingView,
  Platform,
  Image,
} from 'react-native';
import { router, Stack } from 'expo-router';
import { SafeAreaView } from 'react-native-safe-area-context';
import * as Clipboard from 'expo-clipboard';

import { CHAINS, CHAIN_KEYS, type ChainKey } from '../../constants/chains';
import { readTokenContract, isValidAddress } from '../../services/wallet';
import { useTokens, type CustomToken } from '../../hooks/useTokens';
import { useTheme } from '@/theme';
import {
  AccentCard,
  Button,
  BottomActionBar,
  EmptyState,
  Icon,
  IconButton,
  Input,
  PressableScale,
  SegmentedControl,
  SelectRow,
  Sheet,
  Spinner,
  Text,
} from '@/components/ui';

type TabKey = 'token' | 'network';

function hashColor(str: string): string {
  let hash = 0;
  for (let i = 0; i < str.length; i++) {
    hash = str.charCodeAt(i) + ((hash << 5) - hash);
  }
  return `hsl(${Math.abs(hash) % 360}, 55%, 50%)`;
}

const ChainPickerSheet: React.FC<{
  visible: boolean;
  selected: ChainKey;
  onSelect: (key: ChainKey) => void;
  onClose: () => void;
}> = ({ visible, selected, onSelect, onClose }) => {
  const { colors, spacing, radius } = useTheme();
  return (
    <Sheet visible={visible} onClose={onClose} title="Select network">
      <ScrollView
        style={{ flexShrink: 1 }}
        contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.md }}
        keyboardShouldPersistTaps="handled"
      >
        {CHAIN_KEYS.map((key) => {
          const chain = CHAINS[key];
          const active = selected === key;
          return (
            <PressableScale
              key={key}
              onPress={() => {
                onSelect(key);
                onClose();
              }}
              accessibilityRole="button"
              accessibilityState={{ selected: active }}
              style={{
                flexDirection: 'row',
                alignItems: 'center',
                gap: spacing.md,
                paddingVertical: spacing.md,
              }}
            >
              {chain.iconUrl ? (
                <Image
                  source={{ uri: chain.iconUrl }}
                  style={{ width: 28, height: 28, borderRadius: radius.pill }}
                />
              ) : (
                <View
                  style={{
                    width: 28,
                    height: 28,
                    borderRadius: radius.pill,
                    backgroundColor: chain.iconColor,
                  }}
                />
              )}
              <Text variant="body" weight={active ? '600' : '400'} style={{ flex: 1 }}>
                {chain.name}
              </Text>
              {active && <Icon name="check" size={20} color={colors.accent} />}
            </PressableScale>
          );
        })}
      </ScrollView>
    </Sheet>
  );
};

const ImportTokenScreen: React.FC = () => {
  const { colors, spacing } = useTheme();
  const { importToken } = useTokens();

  const [tab, setTab] = useState<TabKey>('token');
  const [chainKey, setChainKey] = useState<ChainKey>('ethereum');
  const [address, setAddress] = useState('');
  const [name, setName] = useState('');
  const [symbol, setSymbol] = useState('');
  const [decimals, setDecimals] = useState('');
  const [fetching, setFetching] = useState(false);
  const [error, setError] = useState('');
  const [showChainPicker, setShowChainPicker] = useState(false);
  const [submitting, setSubmitting] = useState(false);

  const selectedChain = CHAINS[chainKey];

  const isAddressValid = useMemo(() => isValidAddress(address.trim()), [address]);

  const lookup = useCallback(async (addr: string, chain: ChainKey) => {
    setFetching(true);
    setError('');
    try {
      const result = await readTokenContract(addr, chain);
      if (!result) {
        setError('Could not read this contract. Make sure it is a valid ERC-20 on the selected network.');
        return;
      }
      setName(result.name);
      setSymbol(result.symbol);
      setDecimals(String(result.decimals));
    } catch {
      setError('Failed to fetch token data.');
    } finally {
      setFetching(false);
    }
  }, []);

  const handleAddressChange = (text: string) => {
    const trimmed = text.trim();
    setAddress(trimmed);
    setError('');
    if (isValidAddress(trimmed)) {
      void lookup(trimmed, chainKey);
    } else {
      setName('');
      setSymbol('');
      setDecimals('');
    }
  };

  const handleChainChange = (next: ChainKey) => {
    setChainKey(next);
    if (isValidAddress(address.trim())) {
      void lookup(address.trim(), next);
    }
  };

  const handlePaste = async () => {
    const text = await Clipboard.getStringAsync();
    if (text) handleAddressChange(text.trim());
  };

  const handleSave = async () => {
    setError('');
    const addr = address.trim();
    if (!isValidAddress(addr)) {
      setError('Enter a valid contract address.');
      return;
    }
    if (!name.trim() || !symbol.trim() || !decimals.trim()) {
      setError('Name, symbol and decimals are required.');
      return;
    }
    const dec = Number(decimals);
    if (!Number.isInteger(dec) || dec < 0 || dec > 36) {
      setError('Decimals must be a whole number between 0 and 36.');
      return;
    }
    setSubmitting(true);
    try {
      const token: CustomToken = {
        symbol: symbol.trim(),
        name: name.trim(),
        decimals: dec,
        iconColor: hashColor(symbol.trim()),
        chains: [chainKey],
        contractAddress: addr,
        chainKey,
        custom: true,
        importedAt: new Date().toISOString(),
      };
      await importToken(token);
      Alert.alert('Token imported', `${token.symbol} on ${selectedChain.name} has been added.`);
      router.back();
    } catch (err: any) {
      setError(err?.message || 'Failed to import token.');
    } finally {
      setSubmitting(false);
    }
  };

  const canSave = isAddressValid && !!name && !!symbol && !!decimals && !submitting;

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <Stack.Screen
        options={{
          title: 'Import crypto',
          headerRight: () => (
            <IconButton
              icon="info"
              accessibilityLabel="About importing crypto"
              onPress={() =>
                Alert.alert(
                  'Import crypto',
                  'Anyone can create a token on a chain, including fake versions of well-known assets. Verify the contract address before importing.',
                )
              }
            />
          ),
        }}
      />

      {/* Tabs */}
      <View
        style={{
          paddingHorizontal: spacing.lg,
          paddingTop: spacing.md,
          paddingBottom: spacing.sm,
        }}
      >
        <SegmentedControl<TabKey>
          segments={[
            { key: 'token', label: 'Token' },
            { key: 'network', label: 'Network' },
          ]}
          value={tab}
          onChange={setTab}
        />
      </View>

      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === 'ios' ? 'padding' : undefined}
      >
        <ScrollView
          contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
          keyboardShouldPersistTaps="handled"
          showsVerticalScrollIndicator={false}
        >
          {tab === 'token' ? (
            <View style={{ gap: spacing.lg }}>
              <AccentCard padding="md" railColor={colors.warning}>
                <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-start' }}>
                  <Icon name="info" size={18} color={colors.warning} />
                  <Text variant="bodySm" color="textSecondary" style={{ flex: 1 }}>
                    Anyone can create a token, including fake versions of existing tokens.
                    Verify the contract address before importing.
                  </Text>
                </View>
              </AccentCard>

              <SelectRow
                label="Network"
                value={selectedChain.name}
                onPress={() => setShowChainPicker(true)}
                leftElement={
                  selectedChain.iconUrl ? (
                    <Image
                      source={{ uri: selectedChain.iconUrl }}
                      style={{ width: 22, height: 22, borderRadius: 11 }}
                    />
                  ) : (
                    <Icon name="wallet" size={20} />
                  )
                }
              />

              <Input
                label="Address"
                placeholder="0x..."
                value={address}
                onChangeText={handleAddressChange}
                autoCapitalize="none"
                autoCorrect={false}
                multiline
                inputStyle={{ minHeight: 24 }}
                rightAdornment={
                  <PressableScale onPress={handlePaste} hitSlop={8}>
                    <Text variant="bodySm" weight="600" color="accent">
                      Paste
                    </Text>
                  </PressableScale>
                }
              />

              {fetching && (
                <Spinner inline label={`Reading contract on ${selectedChain.name}…`} />
              )}

              <Input
                label="Name"
                value={name}
                onChangeText={setName}
                placeholder="Token name"
                autoCapitalize="words"
              />

              <Input
                label="Symbol"
                value={symbol}
                onChangeText={setSymbol}
                placeholder="e.g. USDC"
                autoCapitalize="characters"
                autoCorrect={false}
              />

              <Input
                label="Decimals"
                value={decimals}
                onChangeText={setDecimals}
                placeholder="e.g. 18"
                keyboardType="number-pad"
              />

              {error ? (
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                  <Icon name="warning" size={16} color={colors.danger} />
                  <Text variant="bodySm" color="danger" style={{ flex: 1 }}>
                    {error}
                  </Text>
                </View>
              ) : null}
            </View>
          ) : (
            <View style={{ paddingTop: spacing['2xl'] }}>
              <EmptyState
                icon="pool"
                title="Custom networks"
                subtitle="Adding custom RPC networks isn't supported yet. Use the supported networks listed in the Token tab."
              />
            </View>
          )}
        </ScrollView>
      </KeyboardAvoidingView>

      {tab === 'token' && (
        <BottomActionBar>
          <Button
            title={`Import ${symbol || 'token'}`}
            onPress={handleSave}
            disabled={!canSave}
            loading={submitting}
            size="lg"
          />
        </BottomActionBar>
      )}

      <ChainPickerSheet
        visible={showChainPicker}
        selected={chainKey}
        onSelect={handleChainChange}
        onClose={() => setShowChainPicker(false)}
      />
    </SafeAreaView>
  );
};

export default ImportTokenScreen;
