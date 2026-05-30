// Payment-window countdown with an urgent (<5 min) state. Self-contained ticker.
import React, { useEffect, useRef, useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Text } from './Text';
import { Icon } from './Icon';

export interface CountdownBarProps {
  /** Expiry as epoch ms, ISO string, or Date. */
  expiresAt: number | string | Date;
  /** Total window length (ms) to render the progress fraction. */
  totalMs?: number;
  onExpire?: () => void;
  label?: string;
}

const URGENT_MS = 5 * 60 * 1000;

function toMs(v: number | string | Date): number {
  if (typeof v === 'number') return v;
  if (v instanceof Date) return v.getTime();
  return new Date(v).getTime();
}

function fmt(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  const pad = (n: number) => String(n).padStart(2, '0');
  return h > 0 ? `${h}:${pad(m)}:${pad(sec)}` : `${pad(m)}:${pad(sec)}`;
}

export const CountdownBar: React.FC<CountdownBarProps> = ({ expiresAt, totalMs, onExpire, label }) => {
  const { colors, radius, spacing } = useTheme();
  const expMs = toMs(expiresAt);
  const [remaining, setRemaining] = useState(() => Math.max(0, expMs - Date.now()));
  const firedRef = useRef(false);

  useEffect(() => {
    const tick = () => {
      const r = Math.max(0, expMs - Date.now());
      setRemaining(r);
      if (r <= 0 && !firedRef.current) {
        firedRef.current = true;
        onExpire?.();
      }
    };
    tick();
    const id = setInterval(tick, 1000);
    return () => clearInterval(id);
  }, [expMs, onExpire]);

  const urgent = remaining <= URGENT_MS && remaining > 0;
  const expired = remaining <= 0;
  const tone = expired ? colors.textMuted : urgent ? colors.danger : colors.escrow;
  const fraction = totalMs && totalMs > 0 ? Math.max(0, Math.min(1, remaining / totalMs)) : undefined;

  return (
    <View style={{ gap: spacing.xs }}>
      <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.xs }}>
        <Icon name="clock" size={14} color={tone} />
        <Text variant="bodySm" color="textSecondary">
          {label ?? (expired ? 'Window elapsed' : 'Time left')}
        </Text>
        <View style={{ flex: 1 }} />
        <Text variant="bodyMed" weight="700" numeric style={{ color: tone }}>
          {expired ? '00:00' : fmt(remaining)}
        </Text>
      </View>
      {fraction !== undefined && (
        <View style={{ height: 4, borderRadius: radius.pill, backgroundColor: colors.surfaceAlt, overflow: 'hidden' }}>
          <View style={{ width: `${fraction * 100}%`, height: 4, backgroundColor: tone }} />
        </View>
      )}
    </View>
  );
};
