import 'react-native-get-random-values';
import { Alert } from "react-native";
import React, { useEffect } from "react";
import { Stack } from "expo-router";
import * as Updates from "expo-updates";
import * as SplashScreen from "expo-splash-screen";
import axios from "axios";
import { AppProvider, useAuth } from "../AuthContext/AuthContext";
import { apiClient } from "../constants/api/apiClient";
import { PROTECTED_ROUTES, PUBLIC_ROUTES } from "@/constants/routeAccess";
import { installSessionTransport } from "@/services/sessionTransport";
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

// Every request to our API — raw fetch, the shared axios instance, bare axios —
// carries, refreshes and expires the session through one place. Installed at
// module load so it is in place before any screen's first request.
installSessionTransport([axios, apiClient]);

// Hold the native splash until the Inter family is ready (design-system fonts).
SplashScreen.preventAutoHideAsync().catch(() => {});

// Screens with the shared header are declared by hand below, in the section
// their access list puts them in (their header options are read by
// __tests__/route-back-affordance.test.ts); every other route is mapped. The
// header is declared here rather than switched on from inside the screen: a
// header turned on after the screen has mounted (as pay.tsx did once auth had
// hydrated) is laid out on Android without the status-bar inset, so its title
// and back chevron sit under the clock. app/__tests__/routeAccess.test.ts keeps
// these lists and the hand-declared screens in step.
const HEADERED_LEAF_SCREENS: readonly string[] = ["activity", "orderdetail", "add-category", "delete-account"];
const HEADERED_PUBLIC_SCREENS: readonly string[] = ["pay"];

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
  const { isAuthenticated, isHydrated } = useAuth();
  // Until the stored session has been read, protected screens stay reachable so
  // a cold-start deep link isn't bounced to index before we know who is signed
  // in; once hydrated, a signed-out user sees nothing but PUBLIC_ROUTES.
  const mayShowProtected = isAuthenticated || !isHydrated;
  return (
    <Stack screenOptions={{ ...headerOptions, headerShown: false }}>
      {/* The mapped list FIRST: when signing out removes the protected
          screens, the stack falls back to its first remaining screen, which
          must be the entry route (index → login). With "pay" first, logout
          landed on an empty "This payment link isn't valid". */}
      {PUBLIC_ROUTES.filter((name) => !HEADERED_PUBLIC_SCREENS.includes(name)).map((name) => (
        <Stack.Screen key={name} name={name} />
      ))}
      <Stack.Screen name="pay" options={{ headerShown: true, title: "Pay" }} />
      <Stack.Protected guard={mayShowProtected}>
        <Stack.Screen name="activity" options={{ headerShown: true, title: "Activity" }} />
        <Stack.Screen name="orderdetail" options={{ headerShown: true, title: "Order Detail" }} />
        <Stack.Screen name="add-category" options={{ headerShown: true, title: "Add Category" }} />
        <Stack.Screen name="delete-account" options={{ headerShown: true, title: "Delete Account" }} />
        {PROTECTED_ROUTES.filter((name) => !HEADERED_LEAF_SCREENS.includes(name)).map((name) => (
          <Stack.Screen key={name} name={name} />
        ))}
      </Stack.Protected>
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
