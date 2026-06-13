// app/(dashboard)/(tabs)/(marketplace)/favourites.tsx
//
// Marketplace — favourites / wishlist. Restyled onto the TSA Signature design
// system (theme tokens + @/components/ui primitives). The favourites data, the
// tap-to-navigate behavior (debounced disable + setCategory + router.push), and
// the navigation route/params are unchanged — this is a visual / structural
// migration only.
//
// NOTE: the favourites item id is `number` here while the legacy ListCard typed
// it as `string`; this mismatch is pre-existing and intentionally preserved.
import { router } from 'expo-router';
import React, { useContext, useState } from 'react';
import { Image, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { AppContext } from '@/AuthContext/AuthContext';
import { useTheme } from '@/theme';
import {
  Card,
  EmptyState,
  Icon,
  PressableScale,
  Text,
} from '@/components/ui';

const favourites = [
  {
    id: 1,
    title: 'MCGP',
    icon: require('../../../../assets/digital/mcgp.png'),
  },
];

// Typed as a plain string (matching the legacy ListCard `itemValue` prop) so the
// `=== 'digital'` guard below stays a valid runtime comparison rather than a
// statically-narrowed dead branch — preserving the original behavior verbatim.
const itemValue: string = 'favourites';

const Favourites = () => {
  const { colors, spacing, radius } = useTheme();
  const { setCategory } = useContext(AppContext);
  const [isDisabled, setIsDisabled] = useState(false);

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        showsVerticalScrollIndicator={false}
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
      >
        <Text variant="title1" weight="700" style={{ marginBottom: spacing.lg }}>
          Favourites
        </Text>

        {favourites.length === 0 ? (
          <EmptyState
            icon="star"
            title="No items found."
            subtitle="Items you favourite will show up here."
          />
        ) : (
          <View style={{ gap: spacing.md }}>
            {favourites.map((item, itemIndex) => (
              <PressableScale
                key={itemIndex}
                disabled={isDisabled}
                onPress={() => {
                  if (itemValue === 'digital') {
                    return alert('coming soon');
                  }
                  if (!isDisabled) {
                    setIsDisabled(true);
                    setCategory(item.title);
                    router.push({
                      // @ts-ignore
                      pathname: `/${itemValue}`,
                      params: { value: item.id },
                    });
                    setTimeout(() => {
                      setIsDisabled(false);
                    }, 2000);
                  }
                }}
              >
                <Card padding="md">
                  <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                    <Image
                      source={item.icon}
                      resizeMode="contain"
                      style={{
                        width: 44,
                        height: 44,
                        borderRadius: radius.pill,
                        backgroundColor: colors.surfaceAlt,
                      }}
                    />
                    <Text variant="bodyMed" weight="600" style={{ flex: 1 }} numberOfLines={1}>
                      {item.title}
                    </Text>
                    <Icon name="chevronRight" size={18} color={colors.textMuted} />
                  </View>
                </Card>
              </PressableScale>
            ))}
          </View>
        )}
      </ScrollView>
    </SafeAreaView>
  );
};

export default Favourites;
