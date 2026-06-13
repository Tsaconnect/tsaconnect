// components/common/SearchBar.tsx
//
// Inline debounced search bar used across marketplace / seller / product lists.
// Migrated onto the TSA Signature design system (theme tokens + @/components/ui
// primitives). The props interface, exports, debounce effect (same hook order),
// onSearch wiring, clear behavior, and all TextInput props are preserved
// verbatim — this is a visual / structural restyle only.
import React, { useState, useEffect } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Icon, Input, PressableScale } from '@/components/ui';

interface SearchBarProps {
  placeholder?: string;
  onSearch: (query: string) => void;
  debounceDelay?: number;
  initialValue?: string;
}

export const SearchBar = ({
  placeholder = 'Search sellers or products...',
  onSearch,
  debounceDelay = 300,
  initialValue = ''
}: SearchBarProps) => {
  const { colors, spacing } = useTheme();
  const [query, setQuery] = useState(initialValue);

  useEffect(() => {
    const timer = setTimeout(() => {
      onSearch(query);
    }, debounceDelay);

    return () => clearTimeout(timer);
  }, [query, debounceDelay, onSearch]);

  const handleClear = () => {
    setQuery('');
    onSearch('');
  };

  return (
    <View style={{ marginBottom: spacing.md, marginHorizontal: spacing.lg }}>
      <Input
        placeholder={placeholder}
        value={query}
        onChangeText={setQuery}
        returnKeyType="search"
        autoCorrect={false}
        leftAdornment={<Icon name="search" size={20} color={colors.textMuted} />}
        rightAdornment={
          query.length > 0 ? (
            <PressableScale onPress={handleClear} hitSlop={8}>
              <Icon name="closeCircle" size={20} color={colors.textMuted} />
            </PressableScale>
          ) : undefined
        }
      />
    </View>
  );
};
