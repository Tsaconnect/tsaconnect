import React, { useState, useEffect, useCallback } from "react";
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
import { useFocusEffect } from "@react-navigation/native";
import api from "@/components/services/api";
import { useAuth } from "../../AuthContext/AuthContext";
import CurrencySelector from "../../components/currency/CurrencySelector";
import {
  isLockEnabled, setLockEnabled, hasPin, removePin,
  isBiometricAvailable, isBiometricEnabled, setBiometricEnabled, getBiometricType,
  authenticateWithBiometric, verifyPin,
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

const SettingsScreen = () => {
  const { logOut, logOutFull, currentUser } = useAuth();
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
    // On Android, Alert.prompt doesn't exist — use biometric with device fallback
    if (pinSet) {
      // Use system biometric prompt which includes device PIN/pattern fallback
      const result = await import('expo-local-authentication').then(m =>
        m.authenticateAsync({
          promptMessage: 'Verify your identity',
          fallbackLabel: 'Use device passcode',
          disableDeviceFallback: false,
        })
      );
      return result.success;
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
    // Verify with biometric before toggling
    if (bioEnabled) {
      // Disabling — verify with biometric first
      const success = await authenticateWithBiometric();
      if (!success) {
        Alert.alert('Verification Failed', `${bioType} verification required to disable.`);
        return;
      }
    } else {
      // Enabling — verify biometric works
      const success = await authenticateWithBiometric();
      if (!success) {
        Alert.alert('Verification Failed', `Could not verify ${bioType}. Please try again.`);
        return;
      }
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

  const handleDeleteAccount = () => {
    Alert.alert(
      "Delete Account",
      "This action is permanent and cannot be undone. All your data will be lost.",
      [
        { text: "Cancel", style: "cancel" },
        {
          text: "Delete",
          style: "destructive",
          onPress: () => {
            // TODO: Implement account deletion API call
            Alert.alert("Coming Soon", "Account deletion will be available soon.");
          },
        },
      ]
    );
  };

  const sections: SettingsSection[] = [
    {
      title: "Currency",
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
      ],
    },
    {
      title: "Help & Support",
      items: [
        {
          icon: "help-outline",
          label: "Contact Support",
          description: "Get in touch with our support team",
          onPress: () =>
            Alert.alert("Support", "Email us at support@tsaconnect.com"),
        },
        {
          icon: "info-outline",
          label: "About TSA Connect",
          description: "App version and information",
          onPress: () => Alert.alert("TSA Connect", "Version 1.0.0"),
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
                      color={item.danger ? "#EF4444" : "#9D6B38"}
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
                  <Icon name="chevron-right" size={22} color="#C0C0C0" />
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

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#F5F5F5",
  },
  section: {
    marginTop: 24,
    paddingHorizontal: 16,
  },
  sectionTitle: {
    fontSize: 14,
    fontWeight: "700",
    color: "#9D6B38",
    textTransform: "uppercase",
    letterSpacing: 0.5,
    marginBottom: 8,
    marginLeft: 4,
  },
  sectionCard: {
    backgroundColor: "#FFFFFF",
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
    borderBottomColor: "#E5E5E5",
  },
  iconContainer: {
    width: 40,
    height: 40,
    borderRadius: 10,
    backgroundColor: "#FDF5ED",
    justifyContent: "center",
    alignItems: "center",
    marginRight: 14,
  },
  iconContainerDanger: {
    backgroundColor: "#FEF2F2",
  },
  textContainer: {
    flex: 1,
  },
  label: {
    fontSize: 15,
    fontWeight: "600",
    color: "#1F2937",
    marginBottom: 2,
  },
  labelDanger: {
    color: "#EF4444",
  },
  description: {
    fontSize: 13,
    color: "#9CA3AF",
  },
});
