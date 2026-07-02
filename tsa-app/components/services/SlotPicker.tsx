// components/services/SlotPicker.tsx
//
// Buyer-facing SLOT PICKER for Booking-type services. Fetches computed open
// slots for the next ~14 days (getServiceSlots), lets the buyer pick a day then
// an open time slot (slots with remaining == 0 are disabled), choose a party
// size, and hands the reservation back to the parent as
// { slotStart, partySize } → bookingSlots[productId] for createOrders().
//
// A legacy fare-only booking (slotDurationMinutes == 0) has no slot grid — the
// parent handles that case and never renders this component. The server
// recomputes the authoritative charge; nothing here is money-authoritative.
//
// Built entirely on the TSA Signature primitives (@/theme + @/components/ui).
import React, { useCallback, useEffect, useMemo, useState } from 'react';
import { View, ScrollView } from 'react-native';
import { useTheme } from '@/theme';
import {
  Badge,
  Button,
  EmptyState,
  IconButton,
  PressableScale,
  Spinner,
  SummaryCard,
  SummaryRow,
  Text,
} from '@/components/ui';
import {
  getServiceSlots,
  WEEKDAY_LABELS,
  type OpenSlot,
} from '../../services/serviceListingApi';

interface SlotPickerProps {
  productId: string;
  /** Called with the chosen slot when the buyer checks out. */
  onCheckout: (slot: { slotStart: string; partySize: number }) => void;
  /** Drives the Pay button's loading spinner while the parent runs escrow. */
  submitting?: boolean;
}

const DAYS_AHEAD = 14;

// Local YYYY-MM-DD for a Date (getServiceSlots takes a date range).
const ymd = (d: Date) =>
  `${d.getFullYear()}-${String(d.getMonth() + 1).padStart(2, '0')}-${String(d.getDate()).padStart(2, '0')}`;

// Group open slots by their local calendar day (YYYY-MM-DD of slot.start).
const dayKeyOf = (iso: string) => ymd(new Date(iso));

const timeLabel = (iso: string) =>
  new Date(iso).toLocaleTimeString([], { hour: 'numeric', minute: '2-digit' });

export const SlotPicker: React.FC<SlotPickerProps> = ({ productId, onCheckout, submitting }) => {
  const { colors, spacing, radius } = useTheme();

  const [loading, setLoading] = useState(true);
  const [error, setError] = useState<string | null>(null);
  const [slots, setSlots] = useState<OpenSlot[]>([]);
  const [maxCapacity, setMaxCapacity] = useState(1);

  const [selectedDay, setSelectedDay] = useState<string | null>(null);
  const [selectedStart, setSelectedStart] = useState<string | null>(null);
  const [partySize, setPartySize] = useState(1);

  const load = useCallback(async () => {
    setLoading(true);
    setError(null);
    try {
      const now = new Date();
      const to = new Date();
      to.setDate(now.getDate() + DAYS_AHEAD);
      const res = await getServiceSlots(productId, ymd(now), ymd(to));
      if (!res.success || !res.data) {
        throw new Error(res.message || 'Failed to load availability');
      }
      setSlots(res.data.slots || []);
      setMaxCapacity(res.data.maxCapacityPerSlot > 0 ? res.data.maxCapacityPerSlot : 1);
    } catch (e: any) {
      setError(e.message || 'Failed to load availability');
    } finally {
      setLoading(false);
    }
  }, [productId]);

  useEffect(() => {
    load();
  }, [load]);

  // Ordered list of days that have at least one slot.
  const days = useMemo(() => {
    const keys = new Set<string>();
    for (const s of slots) keys.add(dayKeyOf(s.start));
    return Array.from(keys).sort();
  }, [slots]);

  // Default to the first available day.
  useEffect(() => {
    if (!selectedDay && days.length > 0) setSelectedDay(days[0]);
  }, [days, selectedDay]);

  const daySlots = useMemo(
    () => (selectedDay ? slots.filter((s) => dayKeyOf(s.start) === selectedDay) : []),
    [slots, selectedDay],
  );

  // The chosen slot's remaining capacity caps party size (-1 = unlimited).
  const selectedSlot = useMemo(
    () => daySlots.find((s) => s.start === selectedStart) || null,
    [daySlots, selectedStart],
  );
  const partyCap = useMemo(() => {
    const rem = selectedSlot?.remaining ?? -1;
    const hardMax = maxCapacity > 0 ? maxCapacity : 1;
    if (rem < 0) return hardMax; // unlimited slot → cap at per-slot capacity
    return Math.max(1, Math.min(hardMax, rem));
  }, [selectedSlot, maxCapacity]);

  // Clamp party size when the selected slot changes.
  useEffect(() => {
    setPartySize((p) => Math.min(Math.max(1, p), partyCap));
  }, [partyCap]);

  const dayHeading = (key: string) => {
    const d = new Date(`${key}T00:00:00`);
    return `${WEEKDAY_LABELS[d.getDay()]} ${d.getDate()}/${d.getMonth() + 1}`;
  };

  const canPay = !!selectedStart;

  if (loading) {
    return (
      <View style={{ paddingVertical: spacing['2xl'], alignItems: 'center' }}>
        <Spinner label="Loading availability…" />
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

  if (days.length === 0) {
    return (
      <EmptyState
        icon="clock"
        title="No open times"
        subtitle="This provider has no bookable slots in the next two weeks."
      />
    );
  }

  return (
    <View style={{ gap: spacing.lg }}>
      <Text variant="bodySm" color="textSecondary">
        Pick a day and time. The final price is confirmed at checkout.
      </Text>

      {/* Day selector */}
      <View>
        <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.xs }}>
          Day
        </Text>
        <ScrollView horizontal showsHorizontalScrollIndicator={false} contentContainerStyle={{ gap: spacing.sm }}>
          {days.map((key) => {
            const active = key === selectedDay;
            return (
              <PressableScale
                key={key}
                onPress={() => {
                  setSelectedDay(key);
                  setSelectedStart(null);
                }}
                style={{
                  paddingVertical: spacing.sm,
                  paddingHorizontal: spacing.md,
                  borderRadius: radius.md,
                  borderWidth: 1.5,
                  borderColor: active ? colors.accent : colors.border,
                  backgroundColor: active ? colors.accentMuted : colors.surfaceAlt,
                }}
              >
                <Text variant="bodySm" weight="600" color={active ? 'accent' : 'textPrimary'}>
                  {dayHeading(key)}
                </Text>
              </PressableScale>
            );
          })}
        </ScrollView>
      </View>

      {/* Time slot grid */}
      <View>
        <Text variant="caption" color="textMuted" style={{ marginBottom: spacing.xs }}>
          Time
        </Text>
        <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm }}>
          {daySlots.map((s) => {
            const full = s.remaining === 0;
            const active = s.start === selectedStart;
            return (
              <PressableScale
                key={s.start}
                disabled={full}
                onPress={() => setSelectedStart(s.start)}
                style={{
                  paddingVertical: spacing.sm,
                  paddingHorizontal: spacing.md,
                  borderRadius: radius.md,
                  borderWidth: 1.5,
                  borderColor: active ? colors.accent : colors.border,
                  backgroundColor: active ? colors.accentMuted : colors.surfaceAlt,
                  opacity: full ? 0.4 : 1,
                }}
              >
                <Text variant="bodySm" weight="600" color={active ? 'accent' : 'textPrimary'}>
                  {timeLabel(s.start)}
                </Text>
                {full ? (
                  <Text variant="overline" color="textMuted">
                    Full
                  </Text>
                ) : s.remaining > 0 ? (
                  <Text variant="overline" color="textMuted">
                    {s.remaining} left
                  </Text>
                ) : null}
              </PressableScale>
            );
          })}
        </View>
      </View>

      {/* Party size */}
      {selectedStart && (
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
          <View style={{ flex: 1 }}>
            <Text variant="bodyMed" weight="600">
              Party size
            </Text>
            <Text variant="caption" color="textMuted">
              Up to {partyCap} per booking
            </Text>
          </View>
          <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
            <IconButton
              icon="minus"
              variant="surface"
              size={16}
              accessibilityLabel="Decrease party size"
              disabled={partySize <= 1}
              onPress={() => setPartySize((p) => Math.max(1, p - 1))}
            />
            <Text variant="bodyMed" weight="700" numeric style={{ minWidth: 20, textAlign: 'center' }}>
              {partySize}
            </Text>
            <IconButton
              icon="plus"
              variant="surface"
              size={16}
              accessibilityLabel="Increase party size"
              disabled={partySize >= partyCap}
              onPress={() => setPartySize((p) => Math.min(partyCap, p + 1))}
            />
          </View>
        </View>
      )}

      {selectedSlot && (
        <SummaryCard>
          <SummaryRow label="Date" value={dayHeading(selectedDay!)} />
          <SummaryRow label="Time" value={timeLabel(selectedSlot.start)} />
          <SummaryRow label="Party size" value={String(partySize)} />
        </SummaryCard>
      )}

      {!canPay && (
        <View style={{ alignItems: 'center' }}>
          <Badge label="Select a time to continue" icon="info" />
        </View>
      )}

      <Button
        title="Pay now"
        size="lg"
        leftIcon="shield"
        onPress={() => selectedStart && onCheckout({ slotStart: selectedStart, partySize })}
        loading={submitting}
        disabled={submitting || !canPay}
      />
    </View>
  );
};

export default SlotPicker;
