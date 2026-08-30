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
import { useDefaultScreenOptions } from "@/constants/navigation";
import { ToastProvider } from "@/components/ui";
import { usePushRegistration } from "../hooks/usePushRegistration";

// Registers the device for Expo push + wires tap-to-navigate. Mounted inside the
// auth tree so it can read the login state; renders nothing.
function PushRegistrar() {
  usePushRegistration();
  return null;
}

// Hold the native splash until the Inter family is ready (design-system fonts).
SplashScreen.preventAutoHideAsync().catch(() => {});

// The root Stack, split out so it can read the theme — useDefaultScreenOptions()
// calls useTheme(), which only resolves inside <ThemeProvider>.
//
// Headers are off by default here: nearly every child is an auth screen or a
// route GROUP that draws its own. The leaf screens below are the exceptions —
// they are pushed from inside the tabs and the drawer, so they need a header
// with a back chevron of their own. `orderdetail` and `add-category` used to be
// Drawer screens, where the only control was a hamburger and the screen drew a
// second header underneath it.
function AppStack() {
  const headerOptions = useDefaultScreenOptions();
  return (
    <Stack screenOptions={{ ...headerOptions, headerShown: false }}>
      <Stack.Screen name="index" options={{ headerShown: false }} />
      <Stack.Screen name="activity" options={{ headerShown: true, title: 'Activity' }} />
      <Stack.Screen name="orderdetail" options={{ headerShown: true, title: 'Order Detail' }} />
      <Stack.Screen name="add-category" options={{ headerShown: true, title: 'Add Category' }} />
    </Stack>
  );
}

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
                  <PushRegistrar />
                  <AppStack />
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
