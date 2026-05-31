// app/(dashboard)/(tabs)/_layout.tsx
//
// Bottom tab navigator for the main app (Home / Marketplace / Services /
// Easyswap / Tokenization / Notifications). Migrated off the legacy
// constants/theme (COLORS) onto the TSA Signature design system: the tab bar
// + header colors and the active/inactive icon tints are now read from
// useTheme() tokens, so the bar adapts to light/dark for free. The exact
// Tabs.Screen registrations, names, order, titles, href gating, and the
// require()'d image icon logic are preserved verbatim — this is a
// visual/structural restyle only.
import { Image } from "react-native";
import { Tabs } from "expo-router";
import { useTheme, fontFamily } from "@/theme";

const DashLayout = () => {
  const { colors } = useTheme();

  // Tint the require()'d PNG icons via tokens: brand accent when focused,
  // muted neutral otherwise. Mirrors the legacy COLORS.primary/COLORS.black
  // split but adapts to the active scheme.
  const iconTint = (focused: boolean) => (focused ? colors.accent : colors.textMuted);

  return (
    <Tabs
      screenOptions={{
        tabBarActiveTintColor: colors.accent,
        tabBarInactiveTintColor: colors.textMuted,
        headerShown: false,
        tabBarStyle: {
          backgroundColor: colors.surface,
          borderTopColor: colors.border,
        },
        tabBarLabelStyle: {
          fontSize: 11,
          fontFamily: fontFamily["500"],
        },
        sceneStyle: {
          backgroundColor: colors.background,
        },
      }}
    >
      <Tabs.Screen
        name="(home)"
        options={{
          title: "Tsa Connect",
          tabBarIcon: ({ color, focused }) => (
            <Image
              source={require("../../../assets/icons/tsa.png")}
              style={{
                height: 25,
                width: 25,
                tintColor: iconTint(focused),
              }}
              resizeMode="contain"
            />
          ),
          headerTintColor: colors.accent,
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="(marketplace)"
        options={{
          title: "Marketplace",
          tabBarIcon: ({ color, focused }) => (
            /*   <TabBarIcon name={focused ? 'home' : 'home-outline'} color={color} /> */
            <Image
              source={require("../../../assets/icons/marketplace.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: iconTint(focused),
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="(services)"
        options={{
          title: "Services",
          tabBarIcon: ({ color, focused }) => (
            /*  <TabBarIcon name={focused ? 'person' : 'person'} color={color} /> */
            <Image
              source={require("../../../assets/icons/service.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: iconTint(focused),
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="easyswap"
        options={{
          title: "Easyswap",
          tabBarIcon: ({ color, focused }) => (
            /*  <TabBarIcon name={focused ? 'person' : 'person'} color={color} /> */
            <Image
              source={require("../../../assets/icons/swap.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: iconTint(focused),
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="tokenization"
        options={{
          title: "Wallet",
          tabBarIcon: ({ color, focused }) => (
            /*  <TabBarIcon name={focused ? 'person' : 'person'} color={color} /> */
            <Image
              source={require("../../../assets/appservices/wallet.png")}
              style={{
                height: 24,
                width: 24,
                tintColor: iconTint(focused),
              }}
              resizeMode="contain"
            />
          ),
          headerShown: false,
        }}
      />
      <Tabs.Screen
        name="notifications"
        options={{
          title: 'Notifications',
          href: null,
        }}
      />
    </Tabs>
  );
};

export default DashLayout;
