// app/(dashboard)/_layout.tsx
//
// Dashboard drawer/stack layout — user (and merchant-extended) navigation drawer
// plus the (tabs) header chrome (notification bell + profile avatar). Migrated onto
// the TSA Signature design system: legacy `COLORS` constants and the
// react-native-elements Avatar are replaced by useTheme() tokens and @/components/ui
// primitives (Text, Avatar, Divider), so the drawer + header chrome track light/dark
// automatically.
//
// LAYOUT INVARIANT: every Drawer.Screen registration, name, order, and option is
// preserved verbatim; the merchant drawer section (its DrawerItems, labels, icons,
// routes, and order) is preserved verbatim. The MaterialCommunityIcons glyphs are
// kept for the drawer/section navigation icons (they are navigation chrome, not
// @/components/ui content icons, and the Icon registry does not cover them). Only
// theming/chrome was swapped onto tokens — auth check, profile fetch, the 5-minute
// refresh interval, refreshProfile, handleAvatarPress, getUserInitials, the
// unauthenticated guard, and the loading/auth render gate are all unchanged.
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

  // Refresh profile function
  const refreshProfile = async () => {
    try {
      if (isAuthenticated) {
        const profileResponse = await api.getProfile();

        if (profileResponse.success && profileResponse.data) {
          setCurrentUser(profileResponse.data);
          return { success: true, data: profileResponse.data };
        } else {
          return { success: false, message: profileResponse.message };
        }
      }
    } catch (error) {
      console.error('Error refreshing profile:', error);
      return { success: false, message: 'Failed to refresh profile' };
    }
  };

  // Handle avatar press - navigate immediately, profile screen fetches its own data
  const handleAvatarPress = () => {
    if (!isAuthenticated) {
      router.push("/login");
      return;
    }
    router.push("/profile");
  };

  // Get user initials for avatar fallback
  const getUserInitials = (name: string) => {
    if (!name) return '?';
    const names = name.split(' ');
    if (names.length === 1) return name.charAt(0).toUpperCase();
    return (names[0].charAt(0) + names[names.length - 1].charAt(0)).toUpperCase();
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

  const merchantIconStyle = {
    marginVertical: -5,
    marginRight: spacing.lg,
    marginLeft: -spacing.md,
  } as const;

  const merchantDrawerLabelStyle = {
    marginHorizontal: -spacing.xl,
    fontSize: type.body.fontSize,
    fontFamily: fontFamily['400'],
    color: colors.textPrimary,
  } as const;

  const CustomDrawerContent = (props: any) => (
    <DrawerContentScrollView {...props}>
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
          <DrawerItem
            label="Merchant Dashboard"
            icon={({ color }) => (
              <Icon name="view-dashboard-outline" size={25} color={color} style={merchantIconStyle} />
            )}
            activeTintColor={colors.accent}
            labelStyle={merchantDrawerLabelStyle}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/dashboard");
            }}
          />
          <DrawerItem
            label="Products & Services"
            icon={({ color }) => (
              <Icon name="shopping-outline" size={25} color={color} style={merchantIconStyle} />
            )}
            activeTintColor={colors.accent}
            labelStyle={merchantDrawerLabelStyle}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/inventory");
            }}
          />
          <DrawerItem
            label="Add Product"
            icon={({ color }) => (
              <Icon name="plus-circle-outline" size={25} color={color} style={merchantIconStyle} />
            )}
            activeTintColor={colors.accent}
            labelStyle={merchantDrawerLabelStyle}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/inventory/add");
            }}
          />
          <DrawerItem
            label="Sell USDT for Fiat"
            icon={({ color }) => (
              <Icon name="trending-up" size={25} color={color} style={merchantIconStyle} />
            )}
            activeTintColor={colors.accent}
            labelStyle={merchantDrawerLabelStyle}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/sell-usdt");
            }}
          />
          <DrawerItem
            label="Buy Crypto with Cash"
            icon={({ color }) => (
              <Icon name="cash-multiple" size={25} color={color} style={merchantIconStyle} />
            )}
            activeTintColor={colors.accent}
            labelStyle={merchantDrawerLabelStyle}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/buy-usdt");
            }}
          />
          <DrawerItem
            label="Digital Assets"
            icon={({ color }) => (
              <Icon name="wallet-outline" size={25} color={color} style={merchantIconStyle} />
            )}
            activeTintColor={colors.accent}
            labelStyle={merchantDrawerLabelStyle}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/digital");
            }}
          />
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
              let iconName;
              switch (route.name) {
                case "categories":
                  iconName = "clipboard-list";
                  break;
                case "profile":
                  iconName = "account-cog";
                  break;
                case "myAdverts":
                  iconName = "shopping";
                  break;
                case "Pending Reviews":
                  iconName = "comment-processing";
                  break;
                case "orderlist":
                  iconName = "ticket-percent";
                  break;
                case "settings":
                  iconName = "cog";
                  break;
                case "Saved Items":
                  iconName = "heart";
                  break;
                case "Followed Sellers":
                  iconName = "account-heart";
                  break;
                case "Recently Viewed":
                  iconName = "eye";
                  break;
                case "(tabs)":
                  iconName = "home";
                  break;
                default:
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
            drawerInactiveTintColor: colors.textPrimary,
            drawerStyle: { backgroundColor: colors.surface },
            drawerLabelStyle: {
              marginHorizontal: -spacing.xl,
              fontSize: type.body.fontSize,
              fontFamily: fontFamily['400'],
              padding: -spacing.xl,
            },
            itemStyle: {
              marginVertical: 0,
            },
            drawerContentContainerStyle: {
              justifyContent: "center",
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
