import React, { useState } from "react";
import {
  StyleSheet,
  Text,
  TextInput,
  View,
  Pressable,
  Alert,
  ActivityIndicator,
  KeyboardAvoidingView,
  Platform,
  ScrollView,
} from "react-native";
import { COLORS } from "../../constants/theme";
import { router } from "expo-router";
import { LinearProgress } from "react-native-elements";
import { useAuth } from "../../AuthContext/AuthContext";
import PhoneNumber from "../country/phoneNumber";
import LocationPicker from "../common/LocationPicker";
import Icon from "@expo/vector-icons/MaterialCommunityIcons";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { generateWallet, addWallet } from "../../services/wallet";
import { registerWalletAddress } from "../../services/walletApi";

interface Country {
  cca2: string;
  callingCode: string;
  name: string;
}

const Signup = () => {
  const [name, setName] = useState("");
  const [referralCode, setReferralCode] = useState("");
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [country, setCountry] = useState("");
  const [phoneNumber, setPhoneNumber] = useState("");
  const [confirmPassword, setConfirmPassword] = useState("");
  const { loading, setLoading, signup } = useAuth();
  const [selectedCountry, setSelectedCountry] = useState<Country | null>(null);
  const [showPassword, setShowPassword] = useState(false);
  const [showConfirmPassword, setShowConfirmPassword] = useState(false);
  const [isError, setIsError] = useState(false);

  function removeAllSpaces(str: string) {
    return str.replace(/\s+/g, "");
  }

  async function handleSignup() {
    if (!name || !email || !country || !phoneNumber || !password || !confirmPassword) {
      Alert.alert("Error", "All fields are required");
      setIsError(true);
      return;
    }

    if (password !== confirmPassword) {
      Alert.alert("Error", "Passwords do not match");
      setIsError(true);
      return;
    }

    setLoading(true);
    setIsError(false);

    const payLoad = {
      name,
      referralCode,
      email: email.trim(),
      password,
      country,
      phoneNumber: removeAllSpaces(
        (selectedCountry?.callingCode || "") + phoneNumber
      ),
    };

    try {
      const result = await signup(payLoad) as any;
      if (result.success) {
        // Auto-generate wallet for new user
        try {
          // Store the auth token first so wallet registration API works
          if (result.data?.token) {
            await AsyncStorage.setItem('authToken', result.data.token);
          }
          const wallet = await generateWallet();
          await addWallet(wallet, 'Wallet 1');
          await registerWalletAddress(wallet.address);
        } catch (walletErr) {
          console.warn('Auto wallet generation failed, user can set up later:', walletErr);
          setLoading(false);
          router.push("/home");
          return;
        }

        setLoading(false);
        // Ask user if they want to back up their seed phrase now
        Alert.alert(
          "Secure Your Wallet",
          "Your wallet has been created. Would you like to back up your seed phrase now? This protects your funds if you lose your device.",
          [
            {
              text: "Later",
              style: "cancel",
              onPress: () => router.push("/home"),
            },
            {
              text: "Back Up Now",
              onPress: () => router.push("/wallet/seedphrase"),
            },
          ]
        );
      } else {
        setIsError(true);
        Alert.alert(
          "Signup Failed",
          result.message || "Could not create account. Please try again.",
          [{ text: "OK" }]
        );
      }
    } catch (err: any) {
      setIsError(true);
      Alert.alert(
        "Connection Error",
        err?.message || "Unable to connect to server. Please check your internet connection.",
        [{ text: "OK" }]
      );
    } finally {
      setLoading(false);
    }
  }

  return (
    <KeyboardAvoidingView
      style={styles.container}
      behavior={Platform.OS === "ios" ? "padding" : "height"}
    >
      <ScrollView
        contentContainerStyle={styles.scrollContent}
        showsVerticalScrollIndicator={false}
        keyboardShouldPersistTaps="handled"
      >
        <Text style={styles.headerText}>Sign Up</Text>

        <View style={styles.formContainer}>
          {/* Name */}
          <TextInput
            style={[styles.input, isError && !name && styles.inputError]}
            value={name}
            placeholder="Full Name"
            onChangeText={(text) => {
              setName(text);
              setIsError(false);
            }}
            autoCapitalize="words"
            editable={!loading}
            placeholderTextColor="#999"
          />

          {/* Phone Number */}
          <PhoneNumber
            inputValue={phoneNumber}
            setInputValue={setPhoneNumber}
            selectedCountry={selectedCountry as Country}
            setSelectedCountry={setSelectedCountry}
          />

          {/* Country Picker */}
          <LocationPicker
            value={{ country, state: "", city: "" }}
            onChange={({ country: c }) => setCountry(c)}
            fields={["country"]}
            required={["country"]}
            showLabels={false}
          />

          {/* Email */}
          <TextInput
            style={[styles.input, isError && !email && styles.inputError]}
            value={email}
            placeholder="Email"
            keyboardType="email-address"
            autoCapitalize="none"
            autoComplete="email"
            onChangeText={(text) => {
              setEmail(text);
              setIsError(false);
            }}
            editable={!loading}
            placeholderTextColor="#999"
          />

          {/* Password */}
          <View
            style={[
              styles.passwordContainer,
              isError && !password && styles.inputError,
            ]}
          >
            <TextInput
              style={styles.passwordInput}
              value={password}
              placeholder="Password"
              secureTextEntry={!showPassword}
              autoCapitalize="none"
              autoComplete="password-new"
              onChangeText={(text) => {
                setPassword(text);
                setIsError(false);
              }}
              editable={!loading}
              placeholderTextColor="#999"
            />
            <Pressable
              style={styles.icon}
              onPress={() => setShowPassword(!showPassword)}
              disabled={loading}
            >
              <Icon
                name={showPassword ? "eye-off" : "eye"}
                size={24}
                color={loading ? "#cccccc" : "gray"}
              />
            </Pressable>
          </View>

          {/* Confirm Password */}
          <View
            style={[
              styles.passwordContainer,
              isError && !confirmPassword && styles.inputError,
              isError &&
                confirmPassword &&
                password !== confirmPassword &&
                styles.inputError,
            ]}
          >
            <TextInput
              style={styles.passwordInput}
              value={confirmPassword}
              placeholder="Confirm Password"
              secureTextEntry={!showConfirmPassword}
              autoCapitalize="none"
              autoComplete="password-new"
              onChangeText={(text) => {
                setConfirmPassword(text);
                setIsError(false);
              }}
              editable={!loading}
              placeholderTextColor="#999"
            />
            <Pressable
              style={styles.icon}
              onPress={() => setShowConfirmPassword(!showConfirmPassword)}
              disabled={loading}
            >
              <Icon
                name={showConfirmPassword ? "eye-off" : "eye"}
                size={24}
                color={loading ? "#cccccc" : "gray"}
              />
            </Pressable>
          </View>

          {/* Referral Code */}
          <TextInput
            style={styles.input}
            value={referralCode}
            placeholder="Referral Code (optional)"
            onChangeText={setReferralCode}
            autoCapitalize="none"
            editable={!loading}
            placeholderTextColor="#999"
          />

          {/* Loading Indicator */}
          {loading && (
            <View style={styles.progressContainer}>
              <LinearProgress color={COLORS.primary} />
              <Text style={styles.loadingText}>Creating account...</Text>
            </View>
          )}

          {/* Signup Button */}
          <Pressable
            onPress={handleSignup}
            style={({ pressed }) => [
              styles.button,
              loading && styles.buttonLoading,
              isError && styles.buttonError,
              pressed && styles.buttonPressed,
            ]}
            disabled={loading}
          >
            {loading ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Text style={styles.buttonText}>Create Account</Text>
            )}
          </Pressable>

          {/* Separator */}
          <View style={styles.separatorContainer}>
            <View style={styles.separatorLine} />
            <Text style={styles.orText}>OR</Text>
            <View style={styles.separatorLine} />
          </View>

          {/* Login Option */}
          <View style={styles.loginContainer}>
            <Text style={styles.question}>Already have an account?</Text>
            <Pressable
              onPress={() => router.push("/login")}
              disabled={loading}
              style={({ pressed }) => [pressed && styles.loginPressed]}
            >
              <Text
                style={[styles.loginLinkText, loading && styles.disabledText]}
              >
                Login
              </Text>
            </Pressable>
          </View>
        </View>
      </ScrollView>
    </KeyboardAvoidingView>
  );
};

export default Signup;

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#fff",
  },
  scrollContent: {
    flexGrow: 1,
    justifyContent: "center",
    padding: 20,
    alignItems: "center",
  },
  formContainer: {
    width: "100%",
    maxWidth: 400,
    alignItems: "center",
  },
  headerText: {
    fontSize: 28,
    fontWeight: "bold",
    marginBottom: 30,
    color: COLORS.primary,
    textAlign: "center",
    width: "100%",
  },
  input: {
    width: "100%",
    height: 56,
    borderColor: "#ddd",
    borderWidth: 1,
    marginBottom: 16,
    paddingHorizontal: 16,
    borderRadius: 12,
    fontSize: 16,
    backgroundColor: "#f9f9f9",
  },
  inputError: {
    borderColor: "#ff4444",
    borderWidth: 2,
    backgroundColor: "#fff5f5",
  },
  passwordContainer: {
    flexDirection: "row",
    alignItems: "center",
    width: "100%",
    height: 56,
    borderColor: "#ddd",
    borderWidth: 1,
    marginBottom: 16,
    borderRadius: 12,
    paddingHorizontal: 16,
    backgroundColor: "#f9f9f9",
  },
  passwordInput: {
    flex: 1,
    fontSize: 16,
    height: "100%",
  },
  icon: {
    padding: 8,
  },
  button: {
    backgroundColor: COLORS.primary,
    width: "100%",
    alignItems: "center",
    justifyContent: "center",
    marginTop: 20,
    height: 56,
    borderRadius: 12,
    borderWidth: 1,
    borderColor: COLORS.primary,
    shadowColor: "#000",
    shadowOffset: { width: 0, height: 2 },
    shadowOpacity: 0.1,
    shadowRadius: 4,
    elevation: 3,
  },
  buttonPressed: {
    opacity: 0.9,
    transform: [{ scale: 0.99 }],
  },
  buttonLoading: {
    backgroundColor: "#d4ba92",
    opacity: 0.8,
  },
  buttonError: {
    borderColor: "#ff4444",
  },
  buttonText: {
    color: "#fff",
    fontSize: 18,
    fontWeight: "600",
  },
  separatorContainer: {
    flexDirection: "row",
    alignItems: "center",
    marginVertical: 24,
    width: "100%",
  },
  separatorLine: {
    flex: 1,
    height: 1,
    backgroundColor: "#ddd",
  },
  orText: {
    marginHorizontal: 16,
    color: "#666",
    fontSize: 14,
    fontWeight: "500",
  },
  loginContainer: {
    flexDirection: "row",
    justifyContent: "center",
    alignItems: "center",
    marginTop: 16,
  },
  loginPressed: {
    opacity: 0.7,
  },
  question: {
    color: "#666",
    fontSize: 14,
    marginRight: 8,
  },
  loginLinkText: {
    color: COLORS.primary,
    fontWeight: "600",
    fontSize: 14,
  },
  disabledText: {
    color: "#cccccc",
  },
  progressContainer: {
    width: "100%",
    marginTop: 10,
    alignItems: "center",
  },
  loadingText: {
    marginTop: 10,
    fontSize: 14,
    color: "#666",
  },
});
