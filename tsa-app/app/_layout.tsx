import 'react-native-get-random-values';
import { Alert } from "react-native";
import React, { useEffect } from "react";
import { Stack } from "expo-router";
import * as Updates from "expo-updates";
import * as SplashScreen from "expo-splash-screen";
import { AppProvider } from "../AuthContext/AuthContext";
import { TokenProvider } from "../hooks/useTokens";
import { NotificationProvider } from '../contexts/NotificationContext';
import { CurrencyProvider } from '../contexts/CurrencyContext';
import { CartProvider } from '../contexts/CartContext';
import { ThemeProvider, ThemedStatusBar, useAppFonts } from "@/theme";
import { ToastProvider } from "@/components/ui";

// Hold the native splash until the Inter family is ready (design-system fonts).
SplashScreen.preventAutoHideAsync().catch(() => {});

const RootLayout = () => {
  const [fontsLoaded] = useAppFonts();

  useEffect(() => {
    if (fontsLoaded) {
      SplashScreen.hideAsync().catch(() => {});
    }
  }, [fontsLoaded]);

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
      } catch {
        // Update check is non-critical, silently ignore
      }
    }, 5000);

    return () => clearTimeout(timer);
  }, []);

  if (!fontsLoaded) return null;

  // ThemeProvider is outermost so every provider + screen can read the theme.
  // ToastProvider is added (innermost) once the Toast primitive lands.
  return (
    <ThemeProvider>
      <AppProvider>
        <TokenProvider>
          <NotificationProvider>
            <CurrencyProvider>
              <CartProvider>
                <ToastProvider>
                  <ThemedStatusBar />
                  <Stack screenOptions={{ headerShown: false }}>
                    <Stack.Screen name="index" options={{ headerShown: false }} />
                    <Stack.Screen name="activity" options={{ headerShown: true, title: 'Activity' }} />
                  </Stack>
                </ToastProvider>
              </CartProvider>
            </CurrencyProvider>
          </NotificationProvider>
        </TokenProvider>
      </AppProvider>
    </ThemeProvider>
  );
};

export default RootLayout;
