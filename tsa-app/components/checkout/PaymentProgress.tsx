// Checkout signing progress as named steps: create the orders, then for each
// order approve the token and lock it in escrow. A multi-order cart shows where
// it is instead of one spinner line that looks stuck.
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Button, Card, Icon, Spinner, Text } from '@/components/ui';

export type PaymentPhase =
  | 'idle'
  | 'creating_orders'
  | 'approving_token'
  | 'creating_escrow'
  | 'submitting'
  | 'done';

interface Step {
  title: string;
  detail: string;
}

export interface PaymentProgressProps {
  token: string;
  /** One entry per order, in signing order. */
  orders: { name: string; amount: string }[];
  phase: PaymentPhase;
  currentOrderIndex: number;
  error: string | null;
  completedCount: number;
  onBackToCheckout: () => void;
  onViewOrders: () => void;
  /**
   * Resumes paying the orders already created. Set once orders exist: their
   * cart is gone, so going back to checkout would find nothing to pay for.
   */
  onRetry?: () => void;
}

/** Index of the active step: 0 = create orders, then two steps per order. */
export function activeStepIndex(phase: PaymentPhase, orderIndex: number, orderCount: number): number {
  switch (phase) {
    case 'creating_orders':
    case 'idle':
      return 0;
    case 'approving_token':
      return 1 + orderIndex * 2;
    case 'creating_escrow':
    case 'submitting':
      return 2 + orderIndex * 2;
    case 'done':
      return 1 + orderCount * 2;
  }
}

export const PaymentProgress = ({
  token,
  orders,
  phase,
  currentOrderIndex,
  error,
  completedCount,
  onBackToCheckout,
  onViewOrders,
  onRetry,
}: PaymentProgressProps) => {
  const { colors, spacing, radius } = useTheme();
  const many = orders.length > 1;
  const steps: Step[] = [
    { title: 'Create orders', detail: 'Reserving stock at the quoted price' },
    ...orders.flatMap((o, i) => [
      { title: `Allow ${token}`, detail: many ? `Order ${i + 1} · ${o.name}` : o.name },
      { title: `Lock ${o.amount} in escrow`, detail: many ? `Order ${i + 1} · confirming on Sonic` : 'Confirming on Sonic' },
    ]),
  ];
  const active = activeStepIndex(phase, currentOrderIndex, orders.length);

  return (
    <View style={{ flex: 1, padding: spacing.lg, gap: spacing.lg }}>
      <Card padding="lg" style={{ gap: spacing.md }}>
        {steps.map((s, i) => {
          const state = i < active ? 'done' : i === active ? (error ? 'failed' : 'now') : 'todo';
          return (
            <View key={i} style={{ flexDirection: 'row', gap: spacing.md, alignItems: 'flex-start' }}>
              <View
                style={{
                  width: 24,
                  height: 24,
                  borderRadius: radius.pill,
                  alignItems: 'center',
                  justifyContent: 'center',
                  backgroundColor:
                    state === 'done'
                      ? colors.successBg
                      : state === 'failed'
                        ? colors.danger
                        : state === 'now'
                          ? colors.accentMuted
                          : colors.surfaceAlt,
                }}
              >
                {state === 'done' ? (
                  <Icon name="check" size={14} color={colors.success} />
                ) : state === 'failed' ? (
                  <Icon name="close" size={14} color={colors.textInverse} />
                ) : state === 'now' ? (
                  <Spinner size="small" inline />
                ) : (
                  <Text variant="caption" weight="700" color="textMuted">
                    {i + 1}
                  </Text>
                )}
              </View>
              <View style={{ flex: 1 }}>
                <Text variant="bodySm" weight="600" color={state === 'todo' ? 'textMuted' : 'textPrimary'}>
                  {s.title}
                </Text>
                <Text variant="caption" color="textMuted" numberOfLines={1}>
                  {s.detail}
                </Text>
              </View>
            </View>
          );
        })}
      </Card>

      {error ? (
        <View style={{ gap: spacing.md }}>
          <Text variant="bodySm" color="danger">
            {error}
          </Text>
          {completedCount > 0 ? (
            <Text variant="caption" color="textSecondary">
              {completedCount} of {orders.length} orders are paid and held in escrow. The others were not paid; you
              can see them all in Orders.
            </Text>
          ) : null}
          {onRetry ? <Button title="Try again" onPress={onRetry} /> : null}
          {onRetry || completedCount > 0 ? (
            <Button title="View orders" variant={onRetry ? 'secondary' : 'primary'} onPress={onViewOrders} />
          ) : (
            <Button title="Back to checkout" onPress={onBackToCheckout} />
          )}
        </View>
      ) : (
        <Text variant="caption" color="textSecondary" align="center">
          Keep the app open. Approve each request when your wallet asks.
        </Text>
      )}
    </View>
  );
};
