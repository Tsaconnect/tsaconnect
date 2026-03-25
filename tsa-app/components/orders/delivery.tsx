import React, { useState } from 'react';
import {
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
  Alert,
} from 'react-native';
import { COLORS, SIZES } from '../../constants/theme';
import { router } from 'expo-router';
import PhoneNumber from '../country/phoneNumber';
import LocationPicker from '../common/LocationPicker';

type DeliveryProps = {
  data: number; // Adjust the type if `data` is an object or other type
};

type Country = {
  name: string;
  callingCode: string;
  flag: string;
};

const Delivery: React.FC<DeliveryProps> = ({ data }) => {
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

  return (
    <View style={{ backgroundColor: '#fff', flex: 1 }}>
      <ScrollView style={styles.cover}>
        <Text
          style={[
            styles.labelHeader,
            { color: COLORS.primary, fontWeight: '600' },
          ]}
        >
          Input Your Delivery Address Information
        </Text>

        <Text style={styles.label}>Enter your Name</Text>
        <TextInput
          placeholder="Your Name"
          style={styles.input}
          value={fullName}
          onChangeText={setFullName}
          numberOfLines={1}
        />

        <LocationPicker value={locationValue} onChange={setLocationValue} />

        <Text style={styles.label}>Enter Delivery Address</Text>
        <TextInput
          placeholder="Your Address"
          style={styles.input}
          value={address}
          onChangeText={setAddress}
          numberOfLines={3}
        />

        <Text style={styles.label}>Enter Phone</Text>
        <PhoneNumber
          //@ts-ignore
          inputValue={phoneNumber}
          setInputValue={setPhoneNumber}
          //@ts-ignore
          selectedCountry={selectedCountry}
          //@ts-ignore
          setSelectedCountry={setSelectedCountry}
        />

        <View style={{ alignItems: 'center', marginBottom: 55 }}>
          <TouchableOpacity
            style={styles.button}
            onPress={() => {
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
            }}
          >
            <Text style={styles.buttonText}>Continue</Text>
          </TouchableOpacity>
        </View>
      </ScrollView>
    </View>
  );
};

export default Delivery;

const styles = StyleSheet.create({
  container: {
    justifyContent: 'center',
    padding: 20,
    backgroundColor: COLORS.white,
  },
  input: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: 'gray',
    marginTop: 2,
    marginBottom: 10,
    padding: 10,
    borderRadius: 5,
    marginHorizontal: SIZES.width * 0.05,
    backgroundColor: COLORS.gray,
  },
  labelHeader: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: 'gray',
    marginTop: 2,
    marginBottom: 10,
    padding: 10,
    marginHorizontal: SIZES.width * 0.05,
  },
  button: {
    backgroundColor: COLORS.primary,
    width: SIZES.width * 0.9,
    alignItems: 'center',
    justifyContent: 'center',
    height: 0.0687 * SIZES.height,
    borderRadius: 10,
    borderWidth: 1,
    borderColor: COLORS.primary,
    marginBottom: 20,
  },
  buttonText: {
    color: '#fff',
    fontSize: 16,
  },
  label: {
    marginHorizontal: SIZES.width * 0.05,
  },
  cover: {
    backgroundColor: COLORS.white,
  },
});
