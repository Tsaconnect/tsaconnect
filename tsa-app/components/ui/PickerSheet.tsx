// Searchable, optionally-grouped picker inside a Sheet — backs bank/provider/
// currency/payment-method selection. Replaces the duplicated picker modals.
import React, { useMemo, useState } from 'react';
import { FlatList, View } from 'react-native';
import { useTheme } from '@/theme';
import { Sheet } from './Sheet';
import { Input } from './Input';
import { Text } from './Text';
import { Icon, IconName } from './Icon';
import { PressableScale } from './PressableScale';
import { Spinner } from './Spinner';
import { EmptyState } from './EmptyState';

export interface PickerItem {
  id: string;
  label: string;
  sublabel?: string;
  icon?: IconName;
  group?: string;
}

export interface PickerSheetProps {
  visible: boolean;
  onClose: () => void;
  title: string;
  items: PickerItem[];
  selectedId?: string;
  onSelect: (item: PickerItem) => void;
  searchable?: boolean;
  loading?: boolean;
  emptyText?: string;
}

type Row = { type: 'header'; group: string } | { type: 'item'; item: PickerItem };

export const PickerSheet: React.FC<PickerSheetProps> = ({
  visible,
  onClose,
  title,
  items,
  selectedId,
  onSelect,
  searchable = true,
  loading,
  emptyText = 'No matches',
}) => {
  const { colors, spacing } = useTheme();
  const [query, setQuery] = useState('');

  const rows = useMemo<Row[]>(() => {
    const q = query.trim().toLowerCase();
    const filtered = q
      ? items.filter((i) => i.label.toLowerCase().includes(q) || i.sublabel?.toLowerCase().includes(q))
      : items;
    const hasGroups = filtered.some((i) => i.group);
    if (!hasGroups) return filtered.map((item) => ({ type: 'item', item }));
    const byGroup = new Map<string, PickerItem[]>();
    for (const i of filtered) {
      const g = i.group ?? 'Other';
      if (!byGroup.has(g)) byGroup.set(g, []);
      byGroup.get(g)!.push(i);
    }
    const out: Row[] = [];
    for (const [group, list] of byGroup) {
      out.push({ type: 'header', group });
      list.forEach((item) => out.push({ type: 'item', item }));
    }
    return out;
  }, [items, query]);

  return (
    <Sheet visible={visible} onClose={onClose} title={title}>
      {searchable && (
        <View style={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm }}>
          <Input
            placeholder="Search"
            value={query}
            onChangeText={setQuery}
            autoCorrect={false}
            leftAdornment={<Icon name="search" size={18} color={colors.textMuted} />}
          />
        </View>
      )}
      {loading ? (
        <View style={{ padding: spacing['3xl'] }}>
          <Spinner label="Loading…" />
        </View>
      ) : rows.length === 0 ? (
        <EmptyState title={emptyText} icon="search" />
      ) : (
        <FlatList
          data={rows}
          style={{ flexShrink: 1 }}
          keyboardShouldPersistTaps="handled"
          keyExtractor={(r, i) => (r.type === 'header' ? `h-${r.group}` : `i-${r.item.id}-${i}`)}
          contentContainerStyle={{ paddingHorizontal: spacing.lg, paddingTop: spacing.sm, paddingBottom: spacing.md }}
          renderItem={({ item: row }) => {
            if (row.type === 'header') {
              return (
                <Text variant="overline" color="textMuted" style={{ marginTop: spacing.md, marginBottom: spacing.xs }}>
                  {row.group.toUpperCase()}
                </Text>
              );
            }
            const it = row.item;
            const selected = it.id === selectedId;
            return (
              <PressableScale
                onPress={() => onSelect(it)}
                accessibilityRole="button"
                accessibilityState={{ selected }}
                style={{
                  flexDirection: 'row',
                  alignItems: 'center',
                  gap: spacing.md,
                  paddingVertical: spacing.md,
                }}
              >
                {it.icon && <Icon name={it.icon} size={20} color={colors.textSecondary} />}
                <View style={{ flex: 1 }}>
                  <Text variant="body" weight={selected ? '600' : '400'}>
                    {it.label}
                  </Text>
                  {it.sublabel && (
                    <Text variant="caption" color="textMuted">
                      {it.sublabel}
                    </Text>
                  )}
                </View>
                {selected && <Icon name="check" size={20} color={colors.accent} />}
              </PressableScale>
            );
          }}
        />
      )}
    </Sheet>
  );
};
