import React, { useState, useCallback } from "react";
import {
  View,
  Text,
  TouchableOpacity,
  StyleSheet,
  ScrollView,
  Alert,
} from "react-native";
import { Icon } from "react-native-elements";
import { router } from "expo-router";
import Constants from "expo-constants";
import * as Updates from "expo-updates";
import { COMPANY } from "@/constants/legal";
import { emailSupport } from "@/utils/contactSupport";
import { useFocusEffect } from "@react-navigation/native";
import { useAuth } from "../../AuthContext/AuthContext";
import CurrencySelector from "../../components/currency/CurrencySelector";
import { SegmentedControl } from "@/components/ui";
import { useThemeControls, useTheme, type SchemePreference } from "@/theme";
import {
  isLockEnabled, setLockEnabled, hasPin, removePin,
  isBiometricAvailable, isBiometricEnabled, setBiometricEnabled, getBiometricType,
  authenticateWithBiometric, authenticateWithDeviceFallback,
} from "@/services/localAuth";

interface SettingsItem {
  icon: string;
  label: string;
  description: string;
  onPress: () => void;
  danger?: boolean;
}

interface SettingsSection {
  title: string;
  items: SettingsItem[];
}

/**
 * What the user is actually running, for support.
 *
 * The marketing version alone cannot answer "did the fix reach you": it only
 * moves when app.json changes, so every over-the-air bundle published between
 * two releases reports the same number. The update id and its publish date are
 * what identify the JS bundle, and the runtime version is what decides which
 * updates this binary is even eligible for.
 */
function buildInfo(): string {
  const lines = [`Version ${Constants.expoConfig?.version ?? "1.0.0"}`];
  if (Updates.runtimeVersion) {
    const channel = Updates.channel ? ` · ${Updates.channel}` : "";
    lines.push(`Runtime ${Updates.runtimeVersion}${channel}`);
  }
  // updateId stays null until an update replaces the bundle the binary shipped
  // with — that state is the answer to "the update never arrived", so name it.
  lines.push(
    Updates.isEmbeddedLaunch || !Updates.updateId
      ? "Update: none (bundled with the app)"
      : `Update ${Updates.updateId.slice(0, 8)} · ${
          Updates.createdAt?.toLocaleString() ?? "date unknown"
        }`
  );
  return lines.join("\n");
}

const SettingsScreen = () => {
  const { logOut, logOutFull } = useAuth();
  const { preference, setScheme } = useThemeControls();
  const { colors } = useTheme();
  const styles = makeStyles(colors);
  const [lockOn, setLockOn] = useState(false);
  const [bioAvailable, setBioAvailable] = useState(false);
  const [bioEnabled, setBioEnabled] = useState(false);
  const [bioType, setBioType] = useState('Biometric');
  const [pinSet, setPinSet] = useState(false);

  // Refresh security state every time settings screen is focused
  useFocusEffect(
    useCallback(() => {
      (async () => {
        setLockOn(await isLockEnabled());
        setPinSet(await hasPin());
        const ba = await isBiometricAvailable();
        setBioAvailable(ba);
        setBioEnabled(await isBiometricEnabled());
        setBioType(await getBiometricType());
      })();
    }, [])
  );

  // Require biometric or PIN before changing security settings
  const requireAuth = async (): Promise<boolean> => {
    if (bioAvailable && bioEnabled) {
      const success = await authenticateWithBiometric();
      if (success) return true;
    }
    // On Android, Alert.prompt doesn't exist — use the system biometric prompt,
    // which includes the device PIN/pattern fallback.
    if (pinSet) {
      return authenticateWithDeviceFallback('Verify your identity');
    }
    return true;
  };

  const handleToggleLock = async () => {
    if (!lockOn) {
      router.push("/pin-setup");
    } else {
      const authed = await requireAuth();
      if (!authed) return;
      Alert.alert("Disable App Lock", "Remove PIN and biometric lock?", [
        { text: "Cancel", style: "cancel" },
        {
          text: "Disable",
          style: "destructive",
          onPress: async () => {
            await setLockEnabled(false);
            await removePin();
            await setBiometricEnabled(false);
            setLockOn(false);
            setPinSet(false);
            setBioEnabled(false);
          },
        },
      ]);
    }
  };

  const handleToggleBiometric = async () => {
    // Verify with biometric before toggling, both to enable and to disable
    let success = false;
    try {
      success = await authenticateWithBiometric();
    } catch {
      success = false;
    }
    if (!success) {
      Alert.alert(
        'Verification Failed',
        bioEnabled
          ? `${bioType} verification required to disable.`
          : `Could not verify ${bioType}. Please try again.`
      );
      return;
    }
    const newVal = !bioEnabled;
    await setBiometricEnabled(newVal);
    setBioEnabled(newVal);
  };

  const handleLogout = () => {
    Alert.alert("Logout", "Are you sure you want to logout?", [
      { text: "Cancel", style: "cancel" },
      {
        text: "Logout",
        style: "destructive",
        onPress: async () => {
          await logOut();
        },
      },
    ]);
  };

  const handleFullLogout = () => {
    Alert.alert(
      "Sign Out of This Device",
      "This will remove biometric login. You'll need your password to sign in again.",
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Sign Out",
          style: "destructive",
          onPress: async () => {
            await logOutFull();
          },
        },
      ]
    );
  };

  // The confirmation, password re-auth and consequences live on the dedicated
  // screen — an Alert can't carry the non-custodial-wallet caveat or the
  // retention notice, and both matter before someone deletes.
  const handleDeleteAccount = () => router.push("/delete-account");

  const sections: SettingsSection[] = [
    {
      title: "Currency",
      items: [],
    },
    {
      title: "Appearance",
      items: [],
    },
    {
      title: "Wallet",
      items: [
        {
          icon: "shield",
          label: "Back Up Seed Phrase",
          description: "Secure your wallet recovery phrase",
          onPress: () => router.push("/wallet/seedphrase"),
        },
        {
          icon: "add-circle-outline",
          label: "Create New Wallet",
          description: "Generate a new wallet with fresh keys",
          onPress: () => router.push("/wallet/manage"),
        },
        {
          icon: "file-download",
          label: "Import Wallet",
          description: "Restore wallet from a 12-word seed phrase",
          onPress: () =>
            router.push({
              pathname: "/wallet/manage",
              params: { mode: "import" },
            }),
        },
      ],
    },
    {
      title: "Security",
      items: [
        {
          icon: lockOn ? "lock" : "lock-open",
          label: lockOn ? "App Lock Enabled" : "Enable App Lock",
          description: lockOn ? "PIN and biometric lock is active" : "Set up PIN or fingerprint to secure the app",
          onPress: handleToggleLock,
        },
        ...(lockOn && bioAvailable
          ? [
              {
                icon: "fingerprint",
                label: `${bioType} Unlock`,
                description: bioEnabled ? `${bioType} is enabled` : `Enable ${bioType} for quick unlock`,
                onPress: handleToggleBiometric,
              },
            ]
          : []),
        ...(lockOn && pinSet
          ? [
              {
                icon: "dialpad",
                label: "Change PIN",
                description: "Update your 4-digit PIN",
                onPress: () => router.push("/pin-setup"),
              },
            ]
          : []),
      ],
    },
    {
      title: "Account & Profile",
      items: [
        {
          icon: "person-outline",
          label: "Profile Information",
          description: "View and edit your profile details",
          onPress: () => router.push("/profile"),
        },
        {
          icon: "verified-user",
          label: "KYC Verification",
          description: "Complete identity verification",
          onPress: () => router.push("/profile/kyc"),
        },
        {
          icon: "history",
          label: "Activity",
          description: "All your transactions in one place",
          onPress: () => router.push("/activity"),
        },
      ],
    },
    {
      title: "Help & Support",
      items: [
        {
          icon: "help-outline",
          label: "Contact Support",
          description: `Email ${COMPANY.supportEmail}`,
          onPress: () => emailSupport(),
        },
        {
          icon: "info-outline",
          label: "About TSA Connect",
          description: "App version and information",
          onPress: () =>
            Alert.alert(
              "TSA Connect",
              `${buildInfo()}` +
                `\n\nOperated by ${COMPANY.legalName} (${COMPANY.registrationNumber}).`
            ),
        },
      ],
    },
    {
      title: "Legal",
      items: [
        {
          icon: "privacy-tip",
          label: "Privacy Policy",
          description: "How we collect, use and protect your data",
          onPress: () => router.push("/legal/privacy"),
        },
        {
          icon: "gavel",
          label: "Terms of Service",
          description: "The agreement governing your use of TSA Connect",
          onPress: () => router.push("/legal/terms"),
        },
      ],
    },
    {
      title: "Danger Zone",
      items: [
        {
          icon: "logout",
          label: "Logout",
          description: "Sign out of your account",
          onPress: handleLogout,
          danger: false,
        },
        {
          icon: "no-encryption",
          label: "Sign Out of This Device",
          description: "Removes biometric login — requires password on next sign in",
          onPress: handleFullLogout,
          danger: true,
        },
        {
          icon: "delete-forever",
          label: "Delete Account",
          description: "Permanently delete your account and data",
          onPress: handleDeleteAccount,
          danger: true,
        },
      ],
    },
  ];

  return (
    <ScrollView style={styles.container} showsVerticalScrollIndicator={false}>
      {sections.map((section) => (
        <View key={section.title} style={styles.section}>
          <Text style={styles.sectionTitle}>{section.title}</Text>
          <View style={styles.sectionCard}>
            {section.title === "Currency" ? (
              <View style={{ padding: 4 }}>
                <CurrencySelector variant="card" showRates />
              </View>
            ) : section.title === "Appearance" ? (
              <View style={{ padding: 12, gap: 8 }}>
                <SegmentedControl
                  segments={[
                    { key: "light", label: "Light" },
                    { key: "dark", label: "Dark" },
                    { key: "system", label: "System" },
                  ]}
                  value={preference}
                  onChange={(k) => setScheme(k as SchemePreference)}
                />
                <Text style={styles.description}>
                  Defaults to light. System follows your device setting.
                </Text>
              </View>
            ) : (
              section.items.map((item, index) => (
                <TouchableOpacity
                  key={item.label}
                  style={[
                    styles.row,
                    index < section.items.length - 1 && styles.rowBorder,
                  ]}
                  onPress={item.onPress}
                  activeOpacity={0.6}
                >
                  <View
                    style={[
                      styles.iconContainer,
                      item.danger && styles.iconContainerDanger,
                    ]}
                  >
                    <Icon
                      name={item.icon}
                      size={22}
                      color={item.danger ? colors.danger : colors.accent}
                    />
                  </View>
                  <View style={styles.textContainer}>
                    <Text
                      style={[styles.label, item.danger && styles.labelDanger]}
                    >
                      {item.label}
                    </Text>
                    <Text style={styles.description}>{item.description}</Text>
                  </View>
                  <Icon name="chevron-right" size={22} color={colors.textMuted} />
                </TouchableOpacity>
              ))
            )}
          </View>
        </View>
      ))}
      <View style={{ height: 40 }} />
    </ScrollView>
  );
};

export default SettingsScreen;

// Theme-aware style factory — colors come from useTheme() so the screen adapts
// to light/dark; layout/typography geometry stays fixed.
const makeStyles = (colors: ReturnType<typeof useTheme>["colors"]) =>
  StyleSheet.create({
    container: {
      flex: 1,
      backgroundColor: colors.background,
    },
    section: {
      marginTop: 24,
      paddingHorizontal: 16,
    },
    sectionTitle: {
      fontSize: 14,
      fontWeight: "700",
      color: colors.accent,
      textTransform: "uppercase",
      letterSpacing: 0.5,
      marginBottom: 8,
      marginLeft: 4,
    },
    sectionCard: {
      backgroundColor: colors.surface,
      borderRadius: 12,
      overflow: "hidden",
    },
    row: {
      flexDirection: "row",
      alignItems: "center",
      paddingVertical: 14,
      paddingHorizontal: 16,
    },
    rowBorder: {
      borderBottomWidth: StyleSheet.hairlineWidth,
      borderBottomColor: colors.hairline,
    },
    iconContainer: {
      width: 40,
      height: 40,
      borderRadius: 10,
      backgroundColor: colors.accentMuted,
      justifyContent: "center",
      alignItems: "center",
      marginRight: 14,
    },
    iconContainerDanger: {
      backgroundColor: colors.dangerBg,
    },
    textContainer: {
      flex: 1,
    },
    label: {
      fontSize: 15,
      fontWeight: "600",
      color: colors.textPrimary,
      marginBottom: 2,
    },
    labelDanger: {
      color: colors.danger,
    },
    description: {
      fontSize: 13,
      color: colors.textMuted,
    },
  });
