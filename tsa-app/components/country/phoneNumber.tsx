import React from "react";
import { View } from "react-native";
import PhoneInput from "react-native-international-phone-number";

interface Country {
  cca2: string;
  callingCode: string;
  name: string;
}

interface PhoneNumberProps {
  selectedCountry: Country;
  setSelectedCountry: (country: Country) => void;
  inputValue: string;
  setInputValue: (value: string) => void;
}

const PhoneNumber: React.FC<PhoneNumberProps> = ({
  selectedCountry,
  setSelectedCountry,
  inputValue,
  setInputValue,
}) => {
  function handleInputValue(phoneNumber: string) {
    setInputValue(phoneNumber);
  }

  function handleSelectedCountry(country: Country) {
    setSelectedCountry(country);
  }

  return (
    <View
      style={{ width: "100%", flex: 1, padding: 10, paddingHorizontal: 20 }}
    >
      <PhoneInput
        value={inputValue}
        onChangePhoneNumber={handleInputValue}
        //@ts-ignore
        selectedCountry={selectedCountry}
        //@ts-ignore
        onChangeSelectedCountry={handleSelectedCountry}
        placeholder="7034567897"
      />
    </View>
  );
};

export default PhoneNumber;
