// components/services/AvailabilityEditor.tsx
//
// Booking/order-type availability editor. Merchants toggle operating days and,
// per active day, add one or more time windows (start/end "HH:MM"). Controlled:
// the parent owns the AvailabilityWindow[] and persists via
// replaceServiceAvailability once the product exists.
//
// TSA Signature design system only (@/theme + @/components/ui).
import React, { useState } from 'react';
import { View } from 'react-native';
import { useTheme } from '@/theme';
import { Button, Card, IconButton, Input, Text, Toggle } from '@/components/ui';
import { AvailabilityWindow, WEEKDAY_LABELS } from '@/services/serviceListingApi';

interface AvailabilityEditorProps {
  windows: AvailabilityWindow[];
  onChange: (windows: AvailabilityWindow[]) => void;
}

const DEFAULT_WINDOW = { startTime: '09:00', endTime: '17:00' };

/** Every day of the week, 09:00–17:00: the starting hours of a new listing. */
export const everyDayWindows = (): AvailabilityWindow[] =>
  WEEKDAY_LABELS.map((_, day) => ({ dayOfWeek: day, ...DEFAULT_WINDOW }));

const AvailabilityEditor: React.FC<AvailabilityEditorProps> = ({ windows, onChange }) => {
  const { colors, spacing } = useTheme();

  const dayActive = (day: number) => windows.some((w) => w.dayOfWeek === day);
  const allDaysActive = WEEKDAY_LABELS.every((_, day) => dayActive(day));

  // Open every day with the same single window: shown as one row of hours
  // instead of seven identical day cards, until the merchant asks for per-day.
  const [perDay, setPerDay] = useState(false);
  const sameHoursEveryDay =
    allDaysActive &&
    windows.length === WEEKDAY_LABELS.length &&
    windows.every((w) => w.startTime === windows[0].startTime && w.endTime === windows[0].endTime);
  const compact = sameHoursEveryDay && !perDay;
  const setAllHours = (patch: Partial<AvailabilityWindow>) => onChange(windows.map((w) => ({ ...w, ...patch })));

  // Switching every day on keeps the hours of days already open.
  const toggleEveryDay = (on: boolean) => {
    if (!on) {
      onChange([]);
      return;
    }
    onChange([...windows, ...everyDayWindows().filter((w) => !dayActive(w.dayOfWeek))]);
  };

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
      <Card padding="lg">
        <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
          <Text variant="bodyMed" weight="700">
            Every day
          </Text>
          <Toggle value={allDaysActive} onValueChange={toggleEveryDay} accessibilityLabel="Toggle every day" />
        </View>
        {compact && (
          <View style={{ gap: spacing.sm, marginTop: spacing.md }}>
            <View style={{ flexDirection: 'row', gap: spacing.sm }}>
              <View style={{ flex: 1 }}>
                <Input
                  label="From"
                  value={windows[0].startTime}
                  onChangeText={(t) => setAllHours({ startTime: t })}
                  placeholder="09:00"
                />
              </View>
              <View style={{ flex: 1 }}>
                <Input
                  label="To"
                  value={windows[0].endTime}
                  onChangeText={(t) => setAllHours({ endTime: t })}
                  placeholder="17:00"
                />
              </View>
            </View>
            <Button title="Set different hours per day" variant="ghost" size="sm" onPress={() => setPerDay(true)} />
          </View>
        )}
      </Card>
      {!compact && WEEKDAY_LABELS.map((label, day) => {
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
