// components/services/MenuPicker.tsx
//
// Buyer-facing MENU PICKER for Order-type services. Lists the merchant's menu
// (listServiceMenu) with a +/- quantity stepper per item, skips unavailable
// items, shows a running (indicative) subtotal, and hands the selection back to
// the parent as menuSelections = { [productId]: [{ itemId, quantity }] } for
// createOrders(). The server recomputes the authoritative charge from the DB —
// the subtotal here is a preview only.
//
// The list renders flat (no internal ScrollView) — the hosting sheet/screen
// provides the single scroll container. With `hideSubmit` + `onSelectionChange`
// the parent owns the CTA instead (pinned sheet footer).
//
// Built entirely on the TSA Signature primitives (@/theme + @/components/ui).
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View, Image } from 'react-native';
import { useTheme } from '@/theme';
import { formatMicroPrice, isMicroPrice } from '@/utils/formatPrice';
import {
  AmountText,
  Button,
  Divider,
  EmptyState,
  IconButton,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import {
  listServiceMenu,
  type ServiceMenuItem,
} from '../../services/serviceListingApi';

interface MenuPickerProps {
  productId: string;
  /** Called with the picked items when the buyer checks out. Empty selection is
   *  blocked by the disabled Pay button, so this always carries ≥1 item. */
  onCheckout?: (items: { itemId: string; quantity: number }[]) => void;
  /** Drives the Pay button's loading spinner while the parent runs escrow. */
  submitting?: boolean;
  /** Hide the internal Pay button — the parent renders the CTA in a pinned
   *  sheet footer and drives checkout from `onSelectionChange`. */
  hideSubmit?: boolean;
  /** Reports the current selection (empty until an item is added) so an
   *  external CTA can enable/disable and check out. */
  onSelectionChange?: (items: { itemId: string; quantity: number }[]) => void;
}

const fmtUsd = (n: number) => (isMicroPrice(n) ? `$${formatMicroPrice(n)}` : `$${n.toFixed(2)}`);

export const MenuPicker: React.FC<MenuPickerProps> = ({
  productId,
  onCheckout,
  submitting,
  hideSubmit,
  onSelectionChange,
}) => {
  const { colors, spacing, radius } = useTheme();

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [items, setItems] = useState<ServiceMenuItem[]>([]);
  // itemId → quantity (0 / absent = not selected).
  const [qty, setQty] = useState<Record<string, number>>({});

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const res = await listServiceMenu(productId);
      if (!res.success || !res.data) {
        throw new Error(res.message || 'Failed to load menu');
      }
      // Skip unavailable items entirely, keep the merchant's sort order.
      const available = (res.data.items || [])
        .filter((it) => it.isAvailable)
        .sort((a, b) => a.sortOrder - b.sortOrder);
      setItems(available);
    } catch (e: any) {
      setError(e.message || 'Failed to load menu');
    } finally {
      setLoading(false);
    }
  }, [productId]);

  useEffect(() => {
    load();
  }, [load]);

  const setItemQty = (itemId: string, next: number) => {
    setQty((prev) => {
      const clamped = Math.max(0, next);
      if (clamped === 0) {
        const { [itemId]: _drop, ...rest } = prev;
        return rest;
      }
      return { ...prev, [itemId]: clamped };
    });
  };

  const selections = useMemo(
    () =>
      Object.entries(qty)
        .filter(([, q]) => q > 0)
        .map(([itemId, quantity]) => ({ itemId, quantity })),
    [qty],
  );

  const subtotal = useMemo(
    () =>
      items.reduce((sum, it) => sum + (qty[it.id] || 0) * (it.priceUsd || 0), 0),
    [items, qty],
  );

  // Mirror the selection up so an external (pinned-footer) CTA can react.
  useEffect(() => {
    onSelectionChange?.(selections);
  }, [selections, onSelectionChange]);

  const canPay = selections.length > 0;

  if (loading) {
    return (
      <View style={{ paddingVertical: spacing['2xl'], alignItems: 'center' }}>
        <Spinner label="Loading menu…" />
      </View>
    );
  }

  if (error) {
    return (
      <View style={{ gap: spacing.md }}>
        <Text variant="bodySm" color="danger">
          {error}
        </Text>
        <Button title="Retry" variant="secondary" onPress={load} />
      </View>
    );
  }

  if (items.length === 0) {
    return (
      <EmptyState
        icon="list"
        title="No items available"
        subtitle="This provider hasn't listed any menu items yet."
      />
    );
  }

  return (
    <View style={{ gap: spacing.lg }}>
      <Text variant="bodySm" color="textSecondary">
        Choose your items and quantities. The final price is confirmed at
        checkout.
      </Text>

      <View>
        {items.map((it, i) => {
          const count = qty[it.id] || 0;
          return (
            <View key={it.id}>
              {i > 0 && <Divider spacing="sm" />}
              <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md, paddingVertical: spacing.xs }}>
                <View
                  style={{
                    width: 48,
                    height: 48,
                    borderRadius: radius.md,
                    backgroundColor: colors.surfaceAlt,
                    justifyContent: 'center',
                    alignItems: 'center',
                    overflow: 'hidden',
                  }}
                >
                  {it.imageUrl ? (
                    <Image source={{ uri: it.imageUrl }} style={{ width: 48, height: 48 }} />
                  ) : (
                    <Text variant="bodySm" color="textMuted">
                      {it.name.slice(0, 1).toUpperCase()}
                    </Text>
                  )}
                </View>

                <View style={{ flex: 1 }}>
                  <Text variant="bodySm" weight="600" numberOfLines={1}>
                    {it.name}
                  </Text>
                  {it.description ? (
                    <Text variant="caption" color="textMuted" numberOfLines={1}>
                      {it.description}
                    </Text>
                  ) : null}
                  <Text variant="caption" color="accent" weight="600" style={{ marginTop: 2 }}>
                    {fmtUsd(it.priceUsd || 0)}
                  </Text>
                </View>

                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
                  <IconButton
                    icon="minus"
                    variant="surface"
                    size={16}
                    accessibilityLabel={`Decrease ${it.name}`}
                    disabled={count === 0}
                    onPress={() => setItemQty(it.id, count - 1)}
                  />
                  <Text variant="bodyMed" weight="700" numeric style={{ minWidth: 20, textAlign: 'center' }}>
                    {count}
                  </Text>
                  <IconButton
                    icon="plus"
                    variant="surface"
                    size={16}
                    accessibilityLabel={`Increase ${it.name}`}
                    onPress={() => setItemQty(it.id, count + 1)}
                  />
                </View>
              </View>
            </View>
          );
        })}
      </View>

      <SummaryCard dividerBeforeLast>
        <SummaryRow
          label="Items"
          value={String(selections.reduce((n, s) => n + s.quantity, 0))}
        />
        <SummaryRow
          label="Subtotal (est.)"
          value={<AmountText value={subtotal} currency="USD" variant="title3" color="accent" />}
          total
        />
      </SummaryCard>

      {!canPay && (
        <Text variant="caption" color="textMuted" align="center">
          Add at least one item to continue.
        </Text>
      )}

      {!hideSubmit && (
        <Button
          title="Pay now"
          size="lg"
          leftIcon="shield"
          onPress={() => onCheckout?.(selections)}
          loading={submitting}
          disabled={submitting || !canPay}
        />
      )}
    </View>
  );
};

export default MenuPicker;
