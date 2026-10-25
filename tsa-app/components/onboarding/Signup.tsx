// Signup form (legacy onboarding component).
//
// Migrated onto the TSA Signature design system: legacy constants/theme styling
// (COLORS) and the react-native-elements LinearProgress chrome replaced with
// @/theme tokens + @/components/ui primitives. ALL logic is preserved verbatim —
// the signup API call, username/password validation, auto wallet generation
// (generateWallet → addWallet → registerWalletAddress) with the AsyncStorage
// auth-token write, the seed-phrase backup prompt, error handling, and every
// navigation route. This is a visual/structural restyle only. The reusable
// PhoneNumber and LocationPicker child components are used unchanged.
import React, { useState } from "react";
import {
  KeyboardAvoidingView,
  Platform,
  ScrollView,
  View,
  Alert,
} from "react-native";
import { SafeAreaView } from "react-native-safe-area-context";
import { router } from "expo-router";
import { useAuth } from "../../AuthContext/AuthContext";
import PhoneNumber from "../country/phoneNumber";
import LocationPicker from "../common/LocationPicker";
import AsyncStorage from "@react-native-async-storage/async-storage";
import { generateWallet, addWallet, removeWallet } from "../../services/wallet";
import { registerWalletAddress } from "../../services/walletApi";
import { useTheme } from "@/theme";
import {
  Button,
  Divider,
  Icon,
  IconButton,
  Input,
  PressableScale,
  Spinner,
  Text,
} from "@/components/ui";

interface Country {
  cca2: string;
  callingCode: string;
  name: string;
}

const Signup = () => {
  const { colors, spacing, radius } = useTheme();
  const [name, setName] = useState("");
  const [username, setUsername] = useState("");
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
    if (!name || !username || !email || !country || !phoneNumber || !password || !confirmPassword) {
      Alert.alert("Error", "All fields are required");
      setIsError(true);
      return;
    }

    const usernameRegex = /^[a-zA-Z0-9_]{3,20}$/;
    if (!usernameRegex.test(username)) {
      Alert.alert("Error", "Username must be 3-20 characters, letters, numbers, or underscores only");
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
      username: username.trim().toLowerCase(),
      referralCode: referralCode?.trim().toLowerCase() || undefined,
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
          // Store, then link; a link that fails (it returns, never throws) is
          // undone so no unlinked key is left behind.
          await addWallet(wallet, 'Wallet 1');
          const registered = await registerWalletAddress(wallet.address, wallet.privateKey);
          if (!registered.success) {
            await removeWallet(wallet.address).catch(() => undefined);
            throw new Error(registered.message || 'Wallet could not be linked');
          }
        } catch (walletErr) {
          console.warn('Auto wallet generation failed, user can set up later:', walletErr);
          setLoading(false);
          Alert.alert(
            "Account created",
            "We couldn't set up your wallet just now. You can create it any time from the Wallet tab.",
            [{ text: "OK", onPress: () => router.push("/home") }],
            { cancelable: false }
          );
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
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={["top"]}>
      <KeyboardAvoidingView
        style={{ flex: 1 }}
        behavior={Platform.OS === "ios" ? "padding" : "height"}
      >
        <ScrollView
          contentContainerStyle={{
            flexGrow: 1,
            justifyContent: "center",
            padding: spacing.lg,
          }}
          showsVerticalScrollIndicator={false}
          keyboardShouldPersistTaps="handled"
        >
          <View style={{ width: "100%", maxWidth: 400, alignSelf: "center" }}>
            <View style={{ alignItems: "center", marginBottom: spacing["3xl"] }}>
              <View
                style={{
                  width: 72,
                  height: 72,
                  borderRadius: radius.pill,
                  backgroundColor: colors.accent,
                  alignItems: "center",
                  justifyContent: "center",
                  marginBottom: spacing.lg,
                }}
              >
                <Text variant="title2" weight="700" style={{ color: colors.onAccent }}>
                  TSA
                </Text>
              </View>
              <Text variant="title1" weight="700" align="center">
                Sign Up
              </Text>
              <Text variant="bodySm" color="textSecondary" style={{ marginTop: spacing.xs }}>
                Create your TSA Connect account
              </Text>
            </View>

            <View>
              {/* Name */}
              <Input
                value={name}
                placeholder="Full Name"
                onChangeText={(text) => {
                  setName(text);
                  setIsError(false);
                }}
                autoCapitalize="words"
                editable={!loading}
                error={isError && !name ? " " : undefined}
                containerStyle={{ marginBottom: spacing.md }}
                leftAdornment={<Icon name="user" size={20} color={colors.textMuted} />}
              />

              {/* Username */}
              <Input
                value={username}
                placeholder="Username"
                onChangeText={(text) => {
                  setUsername(text.replace(/[^a-zA-Z0-9_]/g, ''));
                  setIsError(false);
                }}
                autoCapitalize="none"
                autoCorrect={false}
                editable={!loading}
                maxLength={20}
                error={isError && !username ? " " : undefined}
                containerStyle={{ marginBottom: spacing.md }}
                leftAdornment={<Icon name="user" size={20} color={colors.textMuted} />}
              />

              {/* Phone Number */}
              <View style={{ marginBottom: spacing.md }}>
                <PhoneNumber
                  inputValue={phoneNumber}
                  setInputValue={setPhoneNumber}
                  selectedCountry={selectedCountry as Country}
                  setSelectedCountry={setSelectedCountry}
                />
              </View>

              {/* Country Picker */}
              <View style={{ marginBottom: spacing.md }}>
                <LocationPicker
                  value={{ country, state: "", city: "" }}
                  onChange={({ country: c }) => setCountry(c)}
                  fields={["country"]}
                  required={["country"]}
                  showLabels={false}
                />
              </View>

              {/* Email */}
              <Input
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
                error={isError && !email ? " " : undefined}
                containerStyle={{ marginBottom: spacing.md }}
                leftAdornment={<Icon name="send" size={20} color={colors.textMuted} />}
              />

              {/* Password */}
              <Input
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
                error={isError && !password ? " " : undefined}
                containerStyle={{ marginBottom: spacing.md }}
                leftAdornment={<Icon name="lock" size={20} color={colors.textMuted} />}
                rightAdornment={
                  <IconButton
                    icon={showPassword ? "eye" : "eyeOff"}
                    size={20}
                    color={loading ? colors.textDisabled : colors.textMuted}
                    disabled={loading}
                    onPress={() => setShowPassword(!showPassword)}
                    accessibilityLabel={showPassword ? "Hide password" : "Show password"}
                    style={{ width: 32, height: 32 }}
                  />
                }
              />

              {/* Confirm Password */}
              <Input
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
                error={
                  isError &&
                  (!confirmPassword || (!!confirmPassword && password !== confirmPassword))
                    ? " "
                    : undefined
                }
                containerStyle={{ marginBottom: spacing.md }}
                leftAdornment={<Icon name="lock" size={20} color={colors.textMuted} />}
                rightAdornment={
                  <IconButton
                    icon={showConfirmPassword ? "eye" : "eyeOff"}
                    size={20}
                    color={loading ? colors.textDisabled : colors.textMuted}
                    disabled={loading}
                    onPress={() => setShowConfirmPassword(!showConfirmPassword)}
                    accessibilityLabel={showConfirmPassword ? "Hide password" : "Show password"}
                    style={{ width: 32, height: 32 }}
                  />
                }
              />

              {/* Referral Code */}
              <Input
                value={referralCode}
                placeholder="Referral Code (optional)"
                onChangeText={setReferralCode}
                autoCapitalize="none"
                editable={!loading}
                containerStyle={{ marginBottom: spacing.lg }}
                leftAdornment={<Icon name="localOffer" size={20} color={colors.textMuted} />}
              />

              {/* Loading Indicator */}
              {loading && (
                <View style={{ alignItems: "center", marginBottom: spacing.md }}>
                  <Spinner size="small" label="Creating account..." />
                </View>
              )}

              {/* Signup Button */}
              <Button
                title="Create Account"
                onPress={handleSignup}
                loading={loading}
                disabled={loading}
                size="lg"
              />

              {/* Separator */}
              <View
                style={{
                  flexDirection: "row",
                  alignItems: "center",
                  marginVertical: spacing["2xl"],
                }}
              >
                <View style={{ flex: 1 }}>
                  <Divider />
                </View>
                <Text
                  variant="bodySm"
                  weight="500"
                  color="textMuted"
                  style={{ marginHorizontal: spacing.lg }}
                >
                  OR
                </Text>
                <View style={{ flex: 1 }}>
                  <Divider />
                </View>
              </View>

              {/* Login Option */}
              <View
                style={{
                  flexDirection: "row",
                  justifyContent: "center",
                  alignItems: "center",
                  gap: spacing.sm,
                }}
              >
                <Text variant="bodySm" color="textSecondary">
                  Already have an account?
                </Text>
                <PressableScale
                  onPress={() => router.push("/login")}
                  disabled={loading}
                >
                  <Text
                    variant="bodySm"
                    weight="600"
                    color={loading ? "textDisabled" : "accent"}
                  >
                    Login
                  </Text>
                </PressableScale>
              </View>
            </View>
          </View>
        </ScrollView>
      </KeyboardAvoidingView>
    </SafeAreaView>
  );
};

export default Signup;
