import 'react-native-get-random-values';
import { Alert, StatusBar, StyleSheet } from "react-native";
import React, { useEffect } from "react";
import { Stack } from "expo-router";
import * as Updates from "expo-updates";
import { AppProvider } from "../AuthContext/AuthContext";
import { TokenProvider } from "../hooks/useTokens";
import { NotificationProvider } from '../contexts/NotificationContext';
import { CurrencyProvider } from '../contexts/CurrencyContext';

const RootLayout = () => {
  useEffect(() => {
    if (__DEV__) return;

    // Defer update check so it doesn't block app startup
    const timer = setTimeout(async () => {
      try {
        const update = await Updates.checkForUpdateAsync();
        if (update.isAvailable) {
          await Updates.fetchUpdateAsync();
          Alert.alert(
            "Update Available",
            "A new version has been downloaded. Restart now to apply?",
            [
              { text: "Later", style: "cancel" },
              { text: "Restart", onPress: () => Updates.reloadAsync() },
            ]
          );
        }
      } catch (e) {
        // Update check is non-critical, silently ignore
      }
    }, 5000);

    return () => clearTimeout(timer);
  }, []);

  return (
    <AppProvider>
      <TokenProvider>
        <NotificationProvider>
          <CurrencyProvider>
            <StatusBar barStyle="dark-content" backgroundColor="#fff" />
            <Stack screenOptions={{ headerShown: false }}>
              <Stack.Screen
                name="index"
                options={{
                  headerShown: false,
                }}
              />
            </Stack>
          </CurrencyProvider>
        </NotificationProvider>
      </TokenProvider>
    </AppProvider>
  );
};

export default RootLayout;
