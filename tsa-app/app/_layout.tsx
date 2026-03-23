import 'react-native-get-random-values';
import { Alert, StatusBar, StyleSheet } from "react-native";
import React, { useEffect } from "react";
import { Stack } from "expo-router";
import * as Updates from "expo-updates";
import { AppProvider } from "../AuthContext/AuthContext";
import { TokenProvider } from "../hooks/useTokens";

const RootLayout = () => {
  useEffect(() => {
    async function checkForUpdates() {
      if (__DEV__) return;
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
        console.log("Update check failed:", e);
      }
    }

    checkForUpdates();
  }, []);

  return (
    <AppProvider>
      <TokenProvider>
        <StatusBar barStyle="dark-content" backgroundColor="#fff" />
        <Stack screenOptions={{ headerShown: false }}>
          <Stack.Screen
            name="index"
            options={{
              headerShown: false,
            }}
          />
        </Stack>
      </TokenProvider>
    </AppProvider>
  );
};

export default RootLayout;
