// app/(dashboard)/_layout.tsx
//
// Dashboard drawer/stack layout — user (and merchant-extended) navigation drawer
// plus the (tabs) header chrome (notification bell + profile avatar). Runs on the
// TSA Signature design system (useTheme() tokens + @/components/ui primitives) so
// the drawer + header chrome track light/dark automatically.
//
// Drawer anatomy: identity header (avatar + name + email → profile) · auto items
// from Drawer.Screen registrations · MERCHANT section (role-gated custom
// DrawerItems). Both sections share ONE row treatment (drawerRowIcon /
// drawerLabelStyle / zero item margin) — merchant items must pass an explicit
// inactiveTintColor or their icons render near-invisible in dark mode.
// MaterialCommunityIcons glyphs are navigation chrome, not @/components/ui
// content icons (the Icon registry doesn't cover them); use the -outline
// variants so the set reads as one family. Auth check, profile fetch, the
// 5-minute refresh interval, handleAvatarPress, the unauthenticated guard,
// and the loading/auth render gate are unchanged.
import { Pressable, View } from "react-native";
import React, { useEffect, useState } from "react";
import { Drawer } from "expo-router/drawer";
import {
  DrawerContentScrollView,
  DrawerItemList,
  DrawerItem,
} from "@react-navigation/drawer";
import Icon from "react-native-vector-icons/MaterialCommunityIcons";
import { router } from "expo-router";
import api from "@/components/services/api";
import AsyncStorage from "@react-native-async-storage/async-storage";
import NotificationBell from "../../components/NotificationBell";
import { useTheme, fontFamily } from "@/theme";
import { Avatar, Divider, Text } from "@/components/ui";
import { showDrawerHeader, type NestedNavState } from "@/utils/navHeader";

interface UserProfile {
  _id: string;
  name: string;
  email: string;
  username?: string;
  profilePicture?: string;
  phoneNumber?: string;
}

const Layout = () => {
  const { colors, spacing, radius, type } = useTheme();
  const [currentUser, setCurrentUser] = useState<UserProfile | null>(null);
  const [loading, setLoading] = useState(true);
  const [isAuthenticated, setIsAuthenticated] = useState(false);
  const [userRole, setUserRole] = useState<string | null>(null);

  // Check authentication and fetch user profile on mount
  useEffect(() => {
    const checkAuthAndFetchProfile = async () => {
      try {
        // Check if user is authenticated
        const authenticated = await api.checkAuth();
        setIsAuthenticated(authenticated);

        if (authenticated) {
          // Fetch user profile
          const profileResponse = await api.getProfile();

          if (profileResponse.success && profileResponse.data) {
            setCurrentUser(profileResponse.data);
          } else {
            console.warn('Failed to fetch profile:', profileResponse.message);
          }

          // Get user role
          const role = await AsyncStorage.getItem("role");
          setUserRole(role?.toLowerCase() || null);
        }
      } catch (error) {
        console.error('Error checking auth or fetching profile:', error);
        setIsAuthenticated(false);
      } finally {
        setLoading(false);
      }
    };

    checkAuthAndFetchProfile();

    // Set up a refresh interval for user data (optional)
    const intervalId = setInterval(checkAuthAndFetchProfile, 300000); // Refresh every 5 minutes

    // Clean up interval on unmount
    return () => clearInterval(intervalId);
  }, []);

  // Handle avatar press - navigate immediately, profile screen fetches its own data
  const handleAvatarPress = () => {
    if (!isAuthenticated) {
      router.push("/login");
      return;
    }
    router.push("/profile");
  };

  // Guard: redirect unauthenticated users to login
  useEffect(() => {
    if (!loading && !isAuthenticated) {
      router.replace("/login");
    }
  }, [loading, isAuthenticated]);

  // Don't render dashboard UI until auth is confirmed
  if (loading || !isAuthenticated) {
    return null;
  }

  const isMerchant = userRole === "merchant";

  // ONE row treatment for every drawer row — auto items (Drawer.Screen) and the
  // merchant DrawerItems below both use these, so the two sections can't drift.
  const drawerRowIconStyle = {
    marginVertical: -5,
    marginRight: spacing.lg,
    marginLeft: -spacing.md,
  } as const;

  const drawerLabelStyle = {
    marginHorizontal: -spacing.xl,
    fontSize: type.body.fontSize,
    fontFamily: fontFamily['500'],
  } as const;

  const merchantItems: { label: string; icon: string; route: string }[] = [
    { label: "Merchant Dashboard", icon: "view-dashboard-outline", route: "/merchants/dashboard" },
    { label: "Products & Services", icon: "shopping-outline", route: "/merchants/inventory" },
    { label: "Add Product", icon: "plus-circle-outline", route: "/merchants/inventory/add" },
    { label: "Sell USDT for Fiat", icon: "trending-up", route: "/merchants/sell-usdt" },
    { label: "Buy Crypto with Cash", icon: "cash-multiple", route: "/merchants/buy-usdt" },
    { label: "Digital Assets", icon: "wallet-outline", route: "/merchants/digital" },
  ];

  const CustomDrawerContent = (props: any) => (
    <DrawerContentScrollView {...props}>
      {/* Identity header — who's signed in, tap to manage the account. */}
      <Pressable
        onPress={() => {
          props.navigation.closeDrawer();
          router.push("/profile");
        }}
        accessibilityRole="button"
        accessibilityLabel="Open profile"
        style={{
          flexDirection: "row",
          alignItems: "center",
          gap: spacing.md,
          paddingHorizontal: spacing.xl,
          paddingTop: spacing.sm,
          paddingBottom: spacing.lg,
        }}
      >
        {currentUser?.profilePicture ? (
          <Avatar uri={currentUser.profilePicture} size={44} />
        ) : (
          <Avatar name={currentUser?.name || "?"} size={44} />
        )}
        <View style={{ flex: 1 }}>
          <Text variant="bodyMed" weight="700" numberOfLines={1}>
            {currentUser?.name || "Your account"}
          </Text>
          {currentUser?.email ? (
            <Text variant="caption" color="textMuted" numberOfLines={1}>
              {currentUser.email}
            </Text>
          ) : null}
        </View>
        <Icon name="chevron-right" size={20} color={colors.textMuted} />
      </Pressable>
      <Divider spacing="sm" inset={spacing.xl} />

      <DrawerItemList {...props} />

      {isMerchant && (
        <View style={{ marginTop: spacing.xs }}>
          <Divider spacing="sm" inset={spacing.xl} />
          <Text
            variant="overline"
            color="textMuted"
            style={{ paddingHorizontal: spacing.xl, marginBottom: spacing.xs }}
          >
            MERCHANT
          </Text>
          {merchantItems.map((item) => (
            <DrawerItem
              key={item.route}
              label={item.label}
              icon={({ color }) => (
                <Icon name={item.icon} size={25} color={color} style={drawerRowIconStyle} />
              )}
              activeTintColor={colors.accent}
              // Custom DrawerItems don't inherit screenOptions — without this the
              // icons fall back to react-navigation's dark default and vanish on
              // the dark surface.
              inactiveTintColor={colors.textPrimary}
              labelStyle={drawerLabelStyle}
              style={{ marginVertical: 0 }}
              onPress={() => {
                props.navigation.closeDrawer();
                router.push(item.route as any);
              }}
            />
          ))}
        </View>
      )}
    </DrawerContentScrollView>
  );

  return (
    <>
      <Drawer
        drawerContent={(props) => <CustomDrawerContent {...props} />}
        screenOptions={
          //@ts-ignore
          ({ route, navigation }) => ({
            //@ts-ignore
            drawerIcon: ({ focused, color, size }) => {
              // Outline variants, filled when focused — one visual family for
              // the whole list. Every registered route MUST have a case here;
              // a missing case renders an empty icon slot (misaligned label).
              let iconName;
              switch (route.name) {
                case "(tabs)":
                  iconName = focused ? "home" : "home-outline";
                  break;
                case "orderlist":
                  iconName = focused ? "ticket-percent" : "ticket-percent-outline";
                  break;
                case "myAdverts":
                  iconName = focused ? "shopping" : "shopping-outline";
                  break;
                case "profile":
                  iconName = focused ? "account-cog" : "account-cog-outline";
                  break;
                case "settings":
                  iconName = focused ? "cog" : "cog-outline";
                  break;
                case "categories":
                  iconName = focused ? "clipboard-list" : "clipboard-list-outline";
                  break;
                case "booking-requests":
                  iconName = "calendar-clock";
                  break;
                case "ordering-requests":
                  iconName = focused ? "cart" : "cart-outline";
                  break;
                case "contact-requests":
                  iconName = focused ? "card-account-phone" : "card-account-phone-outline";
                  break;
                case "referrals":
                  iconName = focused ? "account-multiple-plus" : "account-multiple-plus-outline";
                  break;
                case "reward":
                  iconName = focused ? "gift" : "gift-outline";
                  break;
                default:
                  iconName = "circle-small"; // visible placeholder — add a case above
                  break;
              }
              return (
                <Icon
                  //@ts-ignore
                  name={iconName}
                  size={25}
                  color={color}
                  style={{
                    marginVertical: -5,
                    marginRight: spacing.lg,
                    marginLeft: -spacing.md,
                  }}
                />
              );
            },
            headerTintColor: navigation.isFocused() ? colors.accent : colors.textPrimary,
            headerStyle: { backgroundColor: colors.surface },
            headerShadowVisible: false,
            sceneContainerStyle: { backgroundColor: colors.background },
            drawerActiveTintColor: colors.accent,
            drawerActiveBackgroundColor: colors.accentMuted,
            drawerInactiveTintColor: colors.textPrimary,
            drawerStyle: { backgroundColor: colors.surface },
            drawerLabelStyle,
            drawerItemStyle: {
              marginVertical: 0,
            },
          })
        }
      >
        <Drawer.Screen
          name="(tabs)"
          options={
            //@ts-ignore
            ({ navigation, route }) => ({
              headerRight: () => (
              <View style={{ flexDirection: 'row', alignItems: 'center' }}>
                <NotificationBell />
                <Pressable
                  onPress={handleAvatarPress}
                  style={{ flexDirection: "row", alignItems: "center" }}
                >
                  {loading ? (
                    // Loading state
                    <View
                      style={{
                        width: 40,
                        height: 40,
                        borderRadius: radius.pill,
                        backgroundColor: colors.surfaceAlt,
                        justifyContent: 'center',
                        alignItems: 'center',
                        marginRight: spacing.md,
                      }}
                    >
                      <Text variant="bodyMed" weight="700" color="accent">
                        ...
                      </Text>
                    </View>
                  ) : currentUser && currentUser.profilePicture ? (
                    // User has profile picture
                    <View style={{ marginRight: spacing.md }}>
                      <Avatar uri={currentUser.profilePicture} size={36} />
                    </View>
                  ) : currentUser && currentUser.name ? (
                    // User has name but no profile picture
                    <View style={{ marginRight: spacing.md }}>
                      <Avatar name={currentUser.name} size={36} />
                    </View>
                  ) : (
                    // No user data (guest or not logged in)
                    <Pressable
                      onPress={() => {
                        if (!isAuthenticated) {
                          router.push("/login");
                        } else {
                          router.push("/profile");
                        }
                      }}
                      style={{ marginRight: spacing.md }}
                    >
                      <Avatar name="?" size={36} />
                    </Pressable>
                  )}
                </Pressable>
              </View>
              ),
              // The drawer header owns the tab ROOTS only. Once a tab pushes a
              // screen, that screen's own header (back · title · actions) takes
              // over — rendering both stacked two nav bars on every detail page.
              //
              // React Navigation populates `state` on a route hosting a nested
              // navigator, but leaves it off the public RouteProp type.
              headerShown: showDrawerHeader((route as { state?: NestedNavState }).state),
              headerTitle: "TSA Connect",
              title: "Dashboard",
              headerTitleAlign: "center",
              headerTitleStyle: {
                fontSize: type.title3.fontSize,
                fontFamily: fontFamily['600'],
                color: navigation.isFocused() ? colors.accent : colors.textPrimary,
              },
              headerTintColor: navigation.isFocused()
                ? colors.accent
                : colors.textPrimary,
              drawerActiveTintColor: colors.accent,
            })
          }
        />
        <Drawer.Screen
          name="orderlist"
          options={{
            title: "My Orders",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="myAdverts"
          options={{
            title: "My Products",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="profile"
          options={{
            title: "Profile",
            drawerActiveTintColor: colors.accent,
            // Protect profile route if not authenticated
            ...(isAuthenticated ? {} : {
              drawerItemStyle: { display: 'none' as const },
            }),
          }}
        />
        <Drawer.Screen
          name="settings"
          options={{
            title: "Settings",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="categories"
          options={{
            title: "Category",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="booking-requests"
          options={{
            title: "Booking Requests",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="ordering-requests"
          options={{
            title: "Ordering Requests",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="contact-requests"
          options={{
            title: "Contact Requests",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="referrals"
          options={{
            title: "Referrals",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="reward"
          options={{
            title: "Reward",
            drawerActiveTintColor: colors.accent,
          }}
        />
        <Drawer.Screen
          name="orderdetail"
          options={{
            title: "Order Detail",
            drawerItemStyle: { display: "none" },
          }}
        />
        <Drawer.Screen
          name="kyc"
          options={{
            title: "KYC Verification",
            drawerItemStyle: { display: "none" },
          }}
        />
        <Drawer.Screen
          name="add-category"
          options={{
            title: "Add Category",
            drawerItemStyle: { display: "none" },
          }}
        />
      </Drawer>
    </>
  );
};

export default Layout;
