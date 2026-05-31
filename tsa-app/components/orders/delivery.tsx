// components/orders/delivery.tsx
//
// Checkout step 1 — delivery address capture for a product order. Collects the
// buyer's name, location (country/state/city via LocationPicker), street address,
// and phone number, validates that every required field is present, then forwards
// to /paymenttype carrying the order total and the captured shipping details.
//
// Migrated onto the TSA Signature design system: the form is rebuilt on @/theme
// tokens + @/components/ui primitives (Text, Input, Card, Button, BottomActionBar)
// inside a themed SafeAreaView/ScrollView. No legacy COLORS/SIZES remain. The
// state, validation rule, phone normalisation, and the /paymenttype navigation
// (route + params, including totalAmount: data) are preserved verbatim.
import React, { useState } from 'react';
import { Alert, ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { router } from 'expo-router';
import PhoneNumber from '../country/phoneNumber';
import LocationPicker from '../common/LocationPicker';
import { useTheme } from '@/theme';
import {
  BottomActionBar,
  Button,
  Card,
  Icon,
  Input,
  Text,
} from '@/components/ui';

type DeliveryProps = {
  data: number; // Adjust the type if `data` is an object or other type
};

type Country = {
  name: string;
  callingCode: string;
  flag: string;
};

const Delivery: React.FC<DeliveryProps> = ({ data }) => {
  const { colors, spacing, radius } = useTheme();
  const [address, setAddress] = useState<string>('');
  const [locationValue, setLocationValue] = useState({
    country: 'Nigeria',
    state: '',
    city: '',
  });
  const [phoneNumber, setPhoneNumber] = useState<string | undefined>();
  const [selectedCountry, setSelectedCountry] = useState<Country | undefined>();
  const [fullName, setFullName] = useState<string | undefined>();

  const removeAllSpaces = (str: string) => {
    return str.replace(/\s+/g, '');
  };

  const onContinue = () => {
    if (
      !address ||
      !locationValue.state ||
      !locationValue.city ||
      !locationValue.country ||
      !phoneNumber
    ) {
      return Alert.alert('Error', 'Enter every field');
    }
    router.push({
      pathname: '/paymenttype',
      params: {
        address,
        state: locationValue.state,
        city: locationValue.city,
        country: locationValue.country,
        phoneNumber: removeAllSpaces(
          selectedCountry?.callingCode + phoneNumber,
        ),
        totalAmount: data,
        fullName,
      },
    });
  };

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView
        contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}
        keyboardShouldPersistTaps="handled"
      >
        <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.sm, marginBottom: spacing.xs }}>
          <Icon name="nearMe" size={22} color={colors.accent} />
          <Text variant="title2" weight="700">
            Delivery address
          </Text>
        </View>
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
          Tell us where this order should be shipped.
        </Text>

        <Card padding="lg" style={{ gap: spacing.lg }}>
          <Input
            label="Full name"
            placeholder="Your name"
            value={fullName}
            onChangeText={setFullName}
            numberOfLines={1}
          />

          <LocationPicker value={locationValue} onChange={setLocationValue} />

          <Input
            label="Delivery address"
            placeholder="Your address"
            value={address}
            onChangeText={setAddress}
            multiline
            numberOfLines={3}
          />

          <View>
            <Text variant="bodySm" weight="600" color="textSecondary" style={{ marginBottom: spacing.xs }}>
              Phone
            </Text>
            <View
              style={{
                borderWidth: 1.5,
                borderColor: colors.border,
                borderRadius: radius.md,
                backgroundColor: colors.surface,
                overflow: 'hidden',
              }}
            >
              <PhoneNumber
                //@ts-ignore
                inputValue={phoneNumber}
                setInputValue={setPhoneNumber}
                //@ts-ignore
                selectedCountry={selectedCountry}
                //@ts-ignore
                setSelectedCountry={setSelectedCountry}
              />
            </View>
          </View>
        </Card>
      </ScrollView>

      <BottomActionBar>
        <Button title="Continue" rightIcon="arrowRight" onPress={onContinue} />
      </BottomActionBar>
    </SafeAreaView>
  );
};

export default Delivery;
