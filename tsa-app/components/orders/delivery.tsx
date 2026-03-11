import React, { useEffect, useState } from "react";
import {
  Image,
  ScrollView,
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
  Alert,
} from "react-native";
import { COLORS, SIZES } from "../../constants/theme";
import { router } from "expo-router";
import PhoneNumber from "../country/phoneNumber";
import { countries } from "../../constants/api/statesConstants";
import PickerWithSearch from "../country/dropdown";
import DropDownState from "../country/statedropdown";
import CityDropDown from "../country/citydropdown";

type DeliveryProps = {
  data: number; // Adjust the type if `data` is an object or other type
};

type Country = {
  name: string;
  callingCode: string;
  flag: string;
};

type State = {
  name: string;
};

const Delivery: React.FC<DeliveryProps> = ({ data }) => {
  const [address, setAddress] = useState<string>("");
  const [location, setLocation] = useState<string | undefined>();
  const [states, setStates] = useState<State[]>([]);
  const [state, setState] = useState<string | undefined>();
  //@ts-ignore
  const [countryList] = useState<Country[]>(countries);
  const [cities, setCities] = useState<string[]>([]);
  const [phoneNumber, setPhoneNumber] = useState<string | undefined>();
  const [selectedCountry, setSelectedCountry] = useState<Country | undefined>();
  const [fullName, setFullName] = useState<string | undefined>();
  const [country, setCountry] = useState<string>("Nigeria");

  const removeAllSpaces = (str: string) => {
    return str.replace(/\s+/g, "");
  };

  const postData = async (data: { country: string }) => {
    try {
      const response = await fetch(
        "https://countriesnow.space/api/v0.1/countries/states",
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify(data),
        }
      );

      if (!response.ok) {
        throw new Error("Network response was not ok");
      }

      const jsonResponse = await response.json();
      setStates(jsonResponse.data.states);
      return jsonResponse;
    } catch (error) {
      console.error("There was a problem with the fetch operation:", error);
      throw error;
    }
  };

  const getCity = async (data: { country: string; state: string }) => {
    try {
      const response = await fetch(
        "https://countriesnow.space/api/v0.1/countries/state/cities",
        {
          method: "POST",
          headers: {
            "Content-Type": "application/json",
          },
          body: JSON.stringify(data),
        }
      );

      if (!response.ok) {
        throw new Error("Network response was not ok");
      }
      const jsonResponse = await response.json();

      setCities(jsonResponse.data);
      return jsonResponse;
    } catch (error: any) {
      console.error("There was a problem with the fetch operation:", error);
      throw error;
    }
  };

  useEffect(() => {
    if (country) {
      postData({ country });
    }
    if (country && state) {
      getCity({ country, state });
    }
  }, [country, state]);

  return (
    <View style={{ backgroundColor: "#fff", flex: 1 }}>
      <ScrollView style={styles.cover}>
        <Text
          style={[
            styles.labelHeader,
            { color: COLORS.primary, fontWeight: "600" },
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

        <Text style={styles.label}>Enter Country</Text>
        <PickerWithSearch
          //@ts-ignore
          data={countryList}
          selectedItem={country}
          setSelectedItem={setCountry}
          postData={postData}
          backgroundColor={COLORS.gray}
          borderColor={COLORS.gray}
          borderWidth={1}
        />

        <Text style={styles.label}>Enter State</Text>
        <DropDownState
          data={states}
          //@ts-ignore
          selectedItem={state}
          setSelectedItem={setState}
          country={country}
          getCity={getCity}
        />

        <Text style={styles.label}>Enter City</Text>
        <CityDropDown
          data={cities}
          //@ts-ignore
          selectedItem={location}
          setSelectedItem={setLocation}
        />

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

        <View style={{ alignItems: "center", marginBottom: 55 }}>
          <TouchableOpacity
            style={styles.button}
            onPress={() => {
              if (!address || !state || !location || !country || !phoneNumber) {
                return Alert.alert("Error", "Enter every field");
              }
              router.push({
                pathname: "/paymenttype",
                params: {
                  address,
                  state,
                  city: location,
                  country,
                  phoneNumber: removeAllSpaces(
                    selectedCountry?.callingCode + phoneNumber
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
    justifyContent: "center",
    padding: 20,
    backgroundColor: COLORS.white,
  },
  input: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: "gray",
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
    borderColor: "gray",
    marginTop: 2,
    marginBottom: 10,
    padding: 10,
    marginHorizontal: SIZES.width * 0.05,
  },
  button: {
    backgroundColor: COLORS.primary,
    width: SIZES.width * 0.9,
    alignItems: "center",
    justifyContent: "center",
    height: 0.0687 * SIZES.height,
    borderRadius: 10,
    borderWidth: 1,
    borderColor: COLORS.primary,
    marginBottom: 20,
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
  label: {
    marginHorizontal: SIZES.width * 0.05,
  },
  cover: {
    backgroundColor: COLORS.white,
  },
});
