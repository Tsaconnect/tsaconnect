// components/services/AvailabilityEditor.tsx
//
// Booking/order-type availability editor. Merchants toggle operating days and,
// per active day, add one or more time windows (start/end "HH:MM"). Controlled:
// the parent owns the AvailabilityWindow[] and persists via
// replaceServiceAvailability once the product exists.
//
// TSA Signature design system only (@/theme + @/components/ui).
import React from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Button, Card, IconButton, Input, Text, Toggle } from '@/components/ui';
import { AvailabilityWindow, WEEKDAY_LABELS } from '@/services/serviceListingApi';

interface AvailabilityEditorProps {
  windows: AvailabilityWindow[];
  onChange: (windows: AvailabilityWindow[]) => void;
}

const DEFAULT_WINDOW = { startTime: '09:00', endTime: '17:00' };

const AvailabilityEditor: React.FC<AvailabilityEditorProps> = ({ windows, onChange }) => {
  const { colors, spacing } = useTheme();

  const dayActive = (day: number) => windows.some((w) => w.dayOfWeek === day);

  const toggleDay = (day: number, on: boolean) => {
    if (on) {
      onChange([...windows, { dayOfWeek: day, ...DEFAULT_WINDOW }]);
    } else {
      onChange(windows.filter((w) => w.dayOfWeek !== day));
    }
  };

  const addWindow = (day: number) => {
    onChange([...windows, { dayOfWeek: day, ...DEFAULT_WINDOW }]);
  };

  // Update the nth window of a given day (by its position among that day's rows).
  const updateWindow = (day: number, dayIndex: number, patch: Partial<AvailabilityWindow>) => {
    let seen = -1;
    onChange(
      windows.map((w) => {
        if (w.dayOfWeek !== day) return w;
        seen += 1;
        return seen === dayIndex ? { ...w, ...patch } : w;
      })
    );
  };

  const removeWindow = (day: number, dayIndex: number) => {
    let seen = -1;
    onChange(
      windows.filter((w) => {
        if (w.dayOfWeek !== day) return true;
        seen += 1;
        return seen !== dayIndex;
      })
    );
  };

  return (
    <View style={{ gap: spacing.md }}>
      {WEEKDAY_LABELS.map((label, day) => {
        const active = dayActive(day);
        const dayWindows = windows.filter((w) => w.dayOfWeek === day);
        return (
          <Card key={day} padding="lg" style={{ gap: active ? spacing.md : 0 }}>
            <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
              <Text variant="bodyMed" weight="700">
                {label}
              </Text>
              <Toggle
                value={active}
                onValueChange={(on) => toggleDay(day, on)}
                accessibilityLabel={`Toggle ${label}`}
              />
            </View>

            {active &&
              dayWindows.map((w, dayIndex) => (
                <View key={dayIndex} style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'flex-end' }}>
                  <View style={{ flex: 1 }}>
                    <Input
                      label="From"
                      value={w.startTime}
                      onChangeText={(t) => updateWindow(day, dayIndex, { startTime: t })}
                      placeholder="09:00"
                    />
                  </View>
                  <View style={{ flex: 1 }}>
                    <Input
                      label="To"
                      value={w.endTime}
                      onChangeText={(t) => updateWindow(day, dayIndex, { endTime: t })}
                      placeholder="17:00"
                    />
                  </View>
                  <IconButton
                    icon="trash"
                    color={colors.danger}
                    accessibilityLabel={`Remove window for ${label}`}
                    onPress={() => removeWindow(day, dayIndex)}
                  />
                </View>
              ))}

            {active && (
              <Button
                title="Add time window"
                variant="ghost"
                size="sm"
                leftIcon="plus"
                onPress={() => addWindow(day)}
              />
            )}
          </Card>
        );
      })}
    </View>
  );
};

export default AvailabilityEditor;
