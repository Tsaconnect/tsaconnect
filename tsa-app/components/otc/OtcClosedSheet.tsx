// components/otc/OtcClosedSheet.tsx
//
// What a user sees when they tap a paused Buy/Sell on the OTC desk.
//
// A toast was the old mechanism and it is the wrong vessel: the copy runs to
// three clauses and names two other places to trade, which a flash message can
// neither fit nor make actionable. This is a sheet, so the text has room and
// the two destinations are real buttons.
import React from 'react';
import { View } from 'react-native';
import { router, type Href } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Sheet, Text } from '@/components/ui';
import { otcClosedCopy } from '@/constants/otc';
import { RAIL_ROUTES } from '@/constants/rails';

export interface OtcClosedSheetProps {
  /** The paused direction, or null when the sheet is closed. */
  side: 'buy' | 'sell' | null;
  onClose: () => void;
}

export const OtcClosedSheet: React.FC<OtcClosedSheetProps> = ({ side, onClose }) => {
  const { spacing } = useTheme();
  const copy = side ? otcClosedCopy(side) : null;

  // Close first so the destination isn't pushed behind an open sheet — the same
  // ordering AssetPairSheet uses.
  const go = (to: Href) => {
    onClose();
    router.push(to);
  };

  return (
    <Sheet visible={side !== null} onClose={onClose} title={copy?.title} scrollable>
      <View style={{ paddingHorizontal: spacing.lg, paddingBottom: spacing.lg, gap: spacing.lg }}>
        <Text variant="bodyMed" color="textSecondary">
          {copy?.body}
        </Text>

        <View style={{ gap: spacing.sm }}>
          <Button title="Trade on Spot" onPress={() => go('/easyswap?tab=spot')} />
          <Button
            title="Trade on Instant Transfer"
            variant="secondary"
            onPress={() => go(RAIL_ROUTES.it)}
          />
        </View>
      </View>
    </Sheet>
  );
};
