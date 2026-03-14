import { Platform, StatusBar, StyleSheet } from "react-native";
import React, { useEffect } from "react";
import { Stack } from "expo-router";
import { AppProvider } from "../AuthContext/AuthContext";
import { TokenProvider } from "../hooks/useTokens";
const RootLayout = () => {
  useEffect(() => {
    async function configureNavigationBar() {
    }

    configureNavigationBar();
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

const styles = StyleSheet.create({});
