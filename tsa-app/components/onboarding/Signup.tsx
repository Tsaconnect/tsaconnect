import {
  StyleSheet,
  Text,
  TextInput,
  TouchableOpacity,
  View,
} from "react-native";
import React, { useState } from "react";
import { COLORS, SIZES } from "../../constants/theme";
import { router } from "expo-router";
import { LinearProgress } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import PhoneNumber from "../country/phoneNumber";
import { Picker } from "@react-native-picker/picker";
import { countries } from "../../constants/api/statesConstants";
import CustomPickerWithSearch from "../country/dropdown";
import Icon from "@expo/vector-icons/FontAwesome";

const Signup = () => {
  const [name, setName] = useState();
  const [referralCode, setReferralCode] = useState();
  const [email, setEmail] = useState();
  const [password, setPassword] = useState();
  const [country, setCountry] = useState();
  const [phoneNumber, setPhoneNumber] = useState();
  const [confirmPassword, setConfirmPassword] = useState();
  const { loading, setLoading, signup } = useAuth();
  const [selectedCountry, setSelectedCountry] = useState(null);
  const [countryList, setCountryList] = useState(countries || []);

  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);

  function removeAllSpaces(str: string) {
    return str.replace(/\s+/g, "");
  }

  async function handleSignup() {
    if (!country || !password || !confirmPassword || !name || !phoneNumber) {
      alert("All fields needed");
      return;
    }
    setLoading(true);
    const payLoad = {
      name,
      referralCode,
      //@ts-ignore
      email: email.trim(),
      password,
      country,
      //@ts-ignore
      phoneNumber: removeAllSpaces(selectedCountry?.callingCode + phoneNumber),
    };
    if (password != confirmPassword) {
      setLoading(false);
      return alert("Password Mismatch");
    }
    try {
      const result = await signup(payLoad);
      if (result.success) {
        setLoading(false);
        router.push("/home");
      } else {
        alert(result.message);
      }
    } catch (err) {
      alert(err);
      setLoading(false);
    } finally {
      setLoading(false);
    }
  }

  return (
    <View style={styles.container}>
      <View style={styles.cover}>
        <Text style={styles.loginText}>Sign up</Text>
        <TextInput
          value={name}
          style={styles.input}
          placeholder="Name"
          onChangeText={(text: any) => setName(text)}
        />
        <PhoneNumber
          //@ts-ignore
          inputValue={phoneNumber}
          //@ts-ignore
          setInputValue={setPhoneNumber}
          //@ts-ignore
          selectedCountry={selectedCountry}
          //@ts-ignore
          setSelectedCountry={setSelectedCountry}
        />
        <CustomPickerWithSearch
          data={countryList}
          //@ts-ignore
          selectedItem={country}
          //@ts-ignore
          setSelectedItem={setCountry}
          postData={() => {}}
          backgroundColor={"#FFF"}
          borderColor="gray"
          borderWidth={1}
        />
        <TextInput
          style={styles.input}
          value={email}
          placeholder="Email"
          keyboardType="email-address"
          autoCapitalize="none"
          onChangeText={(e: any) => setEmail(e)}
        />
        <View style={styles.passwordContainer}>
          <TextInput
            style={styles.input}
            value={password}
            placeholder="Password"
            secureTextEntry={!showPassword}
            onChangeText={(e: any) => setPassword(e)}
          />
          <TouchableOpacity
            onPress={() => setShowPassword(!showPassword)}
            style={styles.icon}
          >
            <Icon
              name={showPassword ? "eye" : "eye-slash"}
              size={20}
              color="gray"
            />
          </TouchableOpacity>
        </View>
        <View style={styles.passwordContainer}>
          <TextInput
            value={confirmPassword}
            style={styles.input}
            placeholder="Confirm password"
            secureTextEntry={!showConfirmPassword}
            onChangeText={(e: any) => setConfirmPassword(e)}
          />
          <TouchableOpacity
            onPress={() => setShowConfirmPassword(!showConfirmPassword)}
            style={styles.icon}
          >
            <Icon
              name={showConfirmPassword ? "eye" : "eye-slash"}
              size={20}
              color="gray"
            />
          </TouchableOpacity>
        </View>
        <TextInput
          value={referralCode}
          style={styles.input}
          placeholder="Referral Code"
          onChangeText={(text: any) => setReferralCode(text)}
        />
      </View>
      <View>
        {loading && (
          <View style={styles.progressContainer}>
            <LinearProgress color={COLORS.primary} />
            <Text style={styles.loadingText}>Signing in...</Text>
          </View>
        )}
        <TouchableOpacity onPress={handleSignup} style={styles.button}>
          <Text style={styles.buttonText}>Continue</Text>
        </TouchableOpacity>

        <TouchableOpacity
          style={{ alignContent: "center", alignItems: "center" }}
          onPress={() => router.push("/login")}
        >
          <Text style={styles.question}>
            Already have an account?{" "}
            <Text style={styles.signUpText}>Login</Text>
          </Text>
        </TouchableOpacity>
      </View>
    </View>
  );
};

export default Signup;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    justifyContent: "center",
    padding: 20,
    backgroundColor: "#fff",
  },
  loginText: {
    fontSize: 24,
    fontWeight: "bold",
    marginBottom: 20,
    left: "5%",
    color: COLORS.primary,
  },
  input: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: "gray",
    borderWidth: 1,
    marginTop: 10,
    padding: 10,
    borderRadius: 10,
    marginHorizontal: SIZES.width * 0.05,
  },
  passwordContainer: {
    flexDirection: "row",
    alignItems: "center",
    position: "relative",
    borderRadius: 10,
  },
  icon: {
    position: "absolute",
    right: 30,
  },
  pickerContainer: {
    width: SIZES.width * 0.9,
    height: (6.2 / 100) * SIZES.height,
    borderColor: "gray",
    borderWidth: 1,
    marginTop: 10,
    padding: 0,
    borderRadius: 10,
    marginHorizontal: SIZES.width * 0.05,
    justifyContent: "center",
  },
  picker: {
    width: "100%",
    height: "100%",
  },
  button: {
    backgroundColor: COLORS.primary,
    width: SIZES.width * 0.9,
    alignItems: "center",
    justifyContent: "center",
    marginTop: SIZES.height / 1.7,
    height: 0.0687 * SIZES.height,
    borderRadius: 10,
    borderWidth: 1,
    borderColor: COLORS.primary,
  },
  buttonText: {
    color: "#fff",
    fontSize: 16,
  },
  signUpText: {
    color: COLORS.primary,
  },
  question: {
    marginTop: 10,
  },
  cover: {
    position: "absolute",
    top: SIZES.height * 0.1212,
    marginBottom: 15,
  },
  progressContainer: {
    marginTop: 20,
    alignItems: "center",
  },
  loadingText: {
    marginTop: 10,
    fontSize: 16,
  },
});
