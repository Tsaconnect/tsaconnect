import React, { useState, useEffect } from "react";
import {
  StyleSheet,
  Text,
  View,
  TextInput,
  Pressable,
  ActivityIndicator,
  ScrollView,
  TouchableOpacity,
} from "react-native";
import { Ionicons } from "@expo/vector-icons";
import { COLORS } from "../../constants/theme";
import { router, Link } from "expo-router";
import MaterialIcons from "@expo/vector-icons/MaterialIcons";
import api from "../services/api";
import { useAuth } from "../../AuthContext/AuthContext";
import {
  isLockEnabled, isBiometricAvailable,
  authenticateWithBiometric, getBiometricType,
} from "../../services/localAuth";

export default function Login() {
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [isPasswordVisible, setIsPasswordVisible] = useState(false);
  const [loading, setLoading] = useState(false);
  const [emailError, setEmailError] = useState("");
  const [passwordError, setPasswordError] = useState("");
  const [generalError, setGeneralError] = useState("");
  const [showBiometric, setShowBiometric] = useState(false);
  const [bioType, setBioType] = useState("Fingerprint");
  const { setAuthenticated, setEmailVerified, setCurrentUser, setToken: setAuthToken } = useAuth();

  // Check if user can use biometric or PIN login via stored refresh token
  useEffect(() => {
    (async () => {
      const { hasRefreshToken } = await import('../../services/localAuth');
      const hasToken = await hasRefreshToken();
      if (!hasToken) return;

      const lockOn = await isLockEnabled();
      if (!lockOn) return;

      const bioAvail = await isBiometricAvailable();

      // Show biometric if hardware is available
      if (bioAvail) {
        setShowBiometric(true);
        setBioType(await getBiometricType());
      }
    })();
  }, []);

  // Restore session after biometric/PIN verification using refresh token
  const restoreSession = async () => {
    setLoading(true);
    try {
      const refreshResult = await api.refreshSession();
      if (refreshResult.success && refreshResult.data) {
        setAuthenticated(true);
        setAuthToken(refreshResult.data.accessToken);
        try {
          const response = await api.getProfile();
          if (response.success && response.data) {
            setCurrentUser(response.data);
            setEmailVerified(response.data.emailVerified ?? false);
          }
        } catch {}
        router.replace("/home");
        return;
      }

      // Refresh failed — clear and show error
      const { clearRefreshToken } = await import('../../services/localAuth');
      await clearRefreshToken();
      setShowBiometric(false);
      setShowPinLogin(false);
      setGeneralError("Session expired. Please sign in with your password.");
    } catch (error) {
      setGeneralError("Failed to authenticate. Please try again.");
    } finally {
      setLoading(false);
    }
  };

  const handleBiometricLogin = async () => {
    const success = await authenticateWithBiometric();
    if (!success) return;
    await restoreSession();
  };


  function clearErrors() {
    setEmailError("");
    setPasswordError("");
    setGeneralError("");
  }

  async function handleLogin() {
    clearErrors();

    let hasError = false;
    if (!email.trim()) {
      setEmailError("Email or username is required");
      hasError = true;
    }
    if (!password) {
      setPasswordError("Password is required");
      hasError = true;
    }
    if (hasError) return;

    setLoading(true);
    try {
      const response = await api.login(email.trim(), password.trim());
      if (response.success && response.data) {
        // Sync AuthContext with login data
        setAuthenticated(true);
        setAuthToken(response.data.token);
        setEmailVerified(response.data.emailVerified ?? false);
        setCurrentUser(response.data);
        setEmail("");
        setPassword("");
        router.replace("/home");
      } else {
        setGeneralError(
          response.message || "Invalid credentials. Please try again."
        );
      }
    } catch (error: any) {
      setGeneralError(
        api.getErrorMessage(error) ||
          "Unable to connect to server. Please check your internet connection."
      );
    } finally {
      setLoading(false);
    }
  }

  return (
    <ScrollView
      contentContainerStyle={styles.scrollContainer}
      keyboardShouldPersistTaps="handled"
    >
      <View style={styles.container}>
        <View style={styles.brandingContainer}>
          <View style={styles.logoCircle}>
            <Text style={styles.logoText}>TSA</Text>
          </View>
          <Text style={styles.welcomeText}>Welcome Back</Text>
          <Text style={styles.subtitle}>Sign in to your account</Text>
        </View>

        <View style={styles.formContainer}>
          <View style={styles.fieldWrapper}>
            <View
              style={[
                styles.inputContainer,
                emailError ? styles.inputError : null,
              ]}
            >
              <MaterialIcons
                name="email"
                size={20}
                color={emailError ? COLORS.danger : COLORS.gray}
                style={styles.inputIcon}
              />
              <TextInput
                style={styles.input}
                value={email}
                onChangeText={(text) => {
                  setEmail(text);
                  if (emailError) setEmailError("");
                  if (generalError) setGeneralError("");
                }}
                placeholder="Email or Username"
                keyboardType="email-address"
                autoCapitalize="none"
                autoComplete="email"
                editable={!loading}
                placeholderTextColor="#999"
              />
            </View>
            {emailError ? (
              <Text style={styles.errorText}>{emailError}</Text>
            ) : null}
          </View>

          <View style={styles.fieldWrapper}>
            <View
              style={[
                styles.inputContainer,
                passwordError ? styles.inputError : null,
              ]}
            >
              <MaterialIcons
                name="lock"
                size={20}
                color={passwordError ? COLORS.danger : COLORS.gray}
                style={styles.inputIcon}
              />
              <TextInput
                style={styles.input}
                value={password}
                placeholder="Password"
                secureTextEntry={!isPasswordVisible}
                autoCapitalize="none"
                autoComplete="password"
                onChangeText={(text) => {
                  setPassword(text);
                  if (passwordError) setPasswordError("");
                  if (generalError) setGeneralError("");
                }}
                editable={!loading}
                placeholderTextColor="#999"
              />
              <Pressable
                style={styles.passwordToggle}
                onPress={() => setIsPasswordVisible(!isPasswordVisible)}
                disabled={loading}
              >
                <MaterialIcons
                  name={isPasswordVisible ? "visibility" : "visibility-off"}
                  size={20}
                  color={loading ? "#cccccc" : COLORS.gray}
                />
              </Pressable>
            </View>
            {passwordError ? (
              <Text style={styles.errorText}>{passwordError}</Text>
            ) : null}
          </View>

          <Link href="/recovery" asChild disabled={loading}>
            <Pressable style={styles.forgotPasswordButton} disabled={loading}>
              <Text
                style={[styles.forgotPassword, loading && styles.disabledText]}
              >
                Forgot Password?
              </Text>
            </Pressable>
          </Link>

          {generalError ? (
            <View style={styles.generalErrorContainer}>
              <MaterialIcons name="error-outline" size={16} color={COLORS.danger} />
              <Text style={styles.generalErrorText}>{generalError}</Text>
            </View>
          ) : null}

          <Pressable
            onPress={handleLogin}
            style={({ pressed }) => [
              styles.button,
              loading && styles.buttonLoading,
              pressed && styles.buttonPressed,
            ]}
            disabled={loading}
          >
            {loading ? (
              <ActivityIndicator size="small" color="#fff" />
            ) : (
              <Text style={styles.buttonText}>Login</Text>
            )}
          </Pressable>

          {showBiometric && (
            <TouchableOpacity
              style={styles.biometricButton}
              onPress={handleBiometricLogin}
              disabled={loading}
            >
              <Ionicons
                name={bioType === "Face ID" ? "scan" : "finger-print"}
                size={24}
                color={COLORS.primary}
              />
              <Text style={styles.biometricText}>Login with {bioType}</Text>
            </TouchableOpacity>
          )}


          <View style={styles.separatorContainer}>
            <View style={styles.separatorLine} />
            <Text style={styles.orText}>OR</Text>
            <View style={styles.separatorLine} />
          </View>

          <View style={styles.signupContainer}>
            <Text style={styles.question}>New to TSA Connect?</Text>
            <Pressable
              onPress={() => router.push("/signup")}
              disabled={loading}
              style={({ pressed }) => [pressed && styles.signupPressed]}
            >
              <Text
                style={[styles.signUpText, loading && styles.disabledText]}
              >
                Create Account
              </Text>
            </Pressable>
          </View>
        </View>
      </View>
    </ScrollView>
  );
}

const styles = StyleSheet.create({
  scrollContainer: {
    flexGrow: 1,
  },
  container: {
    flex: 1,
    justifyContent: "center",
    padding: 20,
    backgroundColor: "#fff",
    alignItems: "center",
  },
  brandingContainer: {
    alignItems: "center",
    marginBottom: 32,
  },
  logoCircle: {
    width: 72,
    height: 72,
    borderRadius: 36,
    backgroundColor: COLORS.primary,
    alignItems: "center",
    justifyContent: "center",
    marginBottom: 16,
  },
  logoText: {
    color: "#fff",
    fontSize: 24,
    fontWeight: "700",
  },
  welcomeText: {
    fontSize: 28,
    fontWeight: "bold",
    color: COLORS.primary,
    textAlign: "center",
  },
  subtitle: {
    fontSize: 14,
    color: COLORS.gray,
    marginTop: 4,
  },
  formContainer: {
    width: "100%",
    maxWidth: 400,
  },
  fieldWrapper: {
    marginBottom: 4,
  },
  inputContainer: {
    flexDirection: "row",
    alignItems: "center",
    width: "100%",
    height: 56,
    borderColor: COLORS.lightGray,
    borderWidth: 1,
    marginBottom: 2,
    borderRadius: 12,
    paddingHorizontal: 15,
    backgroundColor: "#f9f9f9",
  },
  inputError: {
    borderColor: COLORS.danger,
    borderWidth: 1.5,
    backgroundColor: "#fff5f5",
  },
  inputIcon: {
    marginRight: 10,
  },
  input: {
    flex: 1,
    fontSize: 16,
    height: "100%",
    color: COLORS.dark,
  },
  passwordToggle: {
    padding: 8,
  },
  errorText: {
    color: COLORS.danger,
    fontSize: 12,
    marginLeft: 15,
    marginBottom: 8,
  },
  forgotPasswordButton: {
    alignSelf: "flex-end",
    marginTop: 4,
    marginBottom: 20,
  },
  forgotPassword: {
    color: COLORS.primary,
    fontWeight: "500",
    fontSize: 14,
  },
  disabledText: {
    color: "#cccccc",
  },
  generalErrorContainer: {
    flexDirection: "row",
    alignItems: "center",
    backgroundColor: "#fff5f5",
    borderRadius: 8,
    padding: 12,
    marginBottom: 16,
  },
  generalErrorText: {
    color: COLORS.danger,
    fontSize: 14,
    marginLeft: 8,
    flex: 1,
  },
  button: {
    backgroundColor: COLORS.primary,
    width: "100%",
    alignItems: "center",
    justifyContent: "center",
    height: 56,
    borderRadius: 12,
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
    opacity: 0.8,
  },
  buttonText: {
    color: "#fff",
    fontSize: 18,
    fontWeight: "600",
  },
  biometricButton: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    width: "100%",
    height: 56,
    borderRadius: 12,
    borderWidth: 1.5,
    borderColor: COLORS.primary,
    marginTop: 12,
    gap: 10,
    backgroundColor: "#FFF",
  },
  biometricText: {
    fontSize: 16,
    fontWeight: "600",
    color: COLORS.primary,
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
  signupContainer: {
    flexDirection: "row",
    justifyContent: "center",
    alignItems: "center",
    marginTop: 16,
  },
  signupPressed: {
    opacity: 0.7,
  },
  question: {
    color: "#666",
    fontSize: 14,
    marginRight: 8,
  },
  signUpText: {
    color: COLORS.primary,
    fontWeight: "600",
    fontSize: 14,
  },
});
