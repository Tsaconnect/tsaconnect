// components/spot/SpotListingForm.tsx
//
// Shared create/edit form for admin Spot listings. Mirrors the Category form
// pattern (Input / SelectRow / PickerSheet / Button). Route decides which
// existing app flow the listing's Buy/Sell buttons open.
import React, { useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import {
  Button,
  Input,
  PickerSheet,
  SelectRow,
  Text,
  Toggle,
  useToast,
  type PickerItem,
} from '@/components/ui';
import {
  createSpotListing,
  updateSpotListing,
  type SpotListing,
  type SpotListingInput,
  type SpotRoute,
} from '@/services/spotApi';

// Chain keys match the backend NetworkConfig.Chains / SupportedToken space.
const CHAIN_OPTIONS: PickerItem[] = [
  'sonic', 'bsc', 'ethereum', 'polygon', 'arbitrum', 'base', 'optimism', 'avalanche', 'linea',
].map((c) => ({ id: c, label: c }));

const QUOTE_OPTIONS: PickerItem[] = ['USDT', 'USDC'].map((q) => ({ id: q, label: q }));

const ROUTE_OPTIONS: PickerItem[] = [
  { id: 'mcgp', label: 'MCGP OTC desk (USDC, Contract B)' },
  { id: 'lifi', label: 'All Swap (LiFi, generic)' },
];

interface Props {
  mode: 'create' | 'edit';
  initial?: SpotListing;
}

const SpotListingForm: React.FC<Props> = ({ mode, initial }) => {
  const { colors, spacing } = useTheme();
  const toast = useToast();

  const [baseSymbol, setBaseSymbol] = useState(initial?.baseSymbol ?? '');
  const [quoteSymbol, setQuoteSymbol] = useState(initial?.quoteSymbol ?? 'USDC');
  const [chain, setChain] = useState(initial?.chain ?? 'sonic');
  const [route, setRoute] = useState<SpotRoute>(initial?.route ?? 'lifi');
  const [logoUrl, setLogoUrl] = useState(initial?.logoUrl ?? '');
  const [enabled, setEnabled] = useState(initial?.enabled ?? true);
  const [displayOrder, setDisplayOrder] = useState(String(initial?.displayOrder ?? 0));
  const [saving, setSaving] = useState(false);

  const [quoteSheet, setQuoteSheet] = useState(false);
  const [chainSheet, setChainSheet] = useState(false);
  const [routeSheet, setRouteSheet] = useState(false);

  const routeLabel = ROUTE_OPTIONS.find((r) => r.id === route)?.label;

  const submit = async () => {
    if (!baseSymbol.trim()) {
      toast.error('Enter a base symbol');
      return;
    }
    setSaving(true);
    const body: SpotListingInput = {
      baseSymbol: baseSymbol.trim(),
      quoteSymbol: quoteSymbol.trim(),
      chain,
      route,
      logoUrl: logoUrl.trim(),
      enabled,
      displayOrder: parseInt(displayOrder, 10) || 0,
    };
    const r =
      mode === 'create'
        ? await createSpotListing(body)
        : await updateSpotListing(initial!.id, body);
    setSaving(false);
    if (r.success) {
      toast.success(mode === 'create' ? 'Spot listing created' : 'Spot listing updated');
      router.back();
    } else {
      toast.error(r.message || 'Could not save the listing');
    }
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['bottom']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}>
        <Input
          label="Base symbol *"
          value={baseSymbol}
          onChangeText={setBaseSymbol}
          autoCapitalize="characters"
          placeholder="e.g. MCGP"
        />
        <SelectRow label="Quote asset" value={quoteSymbol} onPress={() => setQuoteSheet(true)} />
        <SelectRow label="Chain" value={chain} onPress={() => setChainSheet(true)} />
        <SelectRow label="Buy/Sell route" value={routeLabel} onPress={() => setRouteSheet(true)} />
        <Input
          label="Logo URL (optional)"
          value={logoUrl}
          onChangeText={setLogoUrl}
          autoCapitalize="none"
          placeholder="https://…"
        />
        <Input
          label="Display order"
          value={displayOrder}
          onChangeText={setDisplayOrder}
          keyboardType="number-pad"
        />
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
          <Text variant="bodyMed" weight="600">
            Enabled
          </Text>
          <Toggle value={enabled} onValueChange={setEnabled} accessibilityLabel="Enabled" />
        </View>
        <Button
          title={mode === 'create' ? 'Create listing' : 'Save changes'}
          onPress={submit}
          loading={saving}
          disabled={saving}
        />
      </ScrollView>

      <PickerSheet
        visible={quoteSheet}
        onClose={() => setQuoteSheet(false)}
        title="Quote asset"
        items={QUOTE_OPTIONS}
        selectedId={quoteSymbol}
        onSelect={(it) => {
          setQuoteSymbol(it.id);
          setQuoteSheet(false);
        }}
      />
      <PickerSheet
        visible={chainSheet}
        onClose={() => setChainSheet(false)}
        title="Chain"
        items={CHAIN_OPTIONS}
        selectedId={chain}
        searchable
        onSelect={(it) => {
          setChain(it.id);
          setChainSheet(false);
        }}
      />
      <PickerSheet
        visible={routeSheet}
        onClose={() => setRouteSheet(false)}
        title="Buy/Sell route"
        items={ROUTE_OPTIONS}
        selectedId={route}
        onSelect={(it) => {
          setRoute(it.id as SpotRoute);
          setRouteSheet(false);
        }}
      />
    </SafeAreaView>
  );
};

export default SpotListingForm;
