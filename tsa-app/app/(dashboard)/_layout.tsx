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
import { DrawerContentScrollView, DrawerItem } from "@react-navigation/drawer";
import { CommonActions, DrawerActions } from "@react-navigation/native";
import Icon from "react-native-vector-icons/MaterialCommunityIcons";
import { router } from "expo-router";
import api from "@/components/services/api";
import AsyncStorage from "@react-native-async-storage/async-storage";
import NotificationBell from "../../components/NotificationBell";
import { useTheme, fontFamily } from "@/theme";
import { Avatar, Divider, Text } from "@/components/ui";
import { isHomeTab, showDrawerHeader, type NestedNavState } from "@/utils/navHeader";
import CartIconBadge from "@/components/common/CartIconBadge";
import { TSA_PAY_APPLY } from "@/constants/routes";
import { useIsPaymentMerchant } from "@/hooks/useIsPaymentMerchant";
import { lpCta } from "@/utils/lpCta";
import { drawerSegments } from "@/utils/drawerSegments";

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
  // Liquidity row: "Provide liquidity & earn", or "Your liquidity" once an LP —
  // the same entry Profile shows, hidden while the pool is off.
  const pm = useIsPaymentMerchant();

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
  // The avatar IS the menu: it opens the drawer, whose first row (you) opens
  // Profile. One account control in the header instead of ☰ + avatar.
  const handleAvatarPress = (navigation: { openDrawer: () => void }) => {
    if (!isAuthenticated) {
      router.push("/login");
      return;
    }
    navigation.openDrawer();
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

  const merchantItems: DrawerLink[] = [
    { label: "Merchant Dashboard", icon: "view-dashboard-outline", route: "/merchants/dashboard" },
    { label: "Products & Services", icon: "shopping-outline", route: "/merchants/inventory" },
    { label: "Add Product", icon: "plus-circle-outline", route: "/merchants/inventory/add" },
    { label: "Sell USDT for Fiat", icon: "trending-up", route: "/merchants/sell-usdt" },
    { label: "Buy Crypto with Cash", icon: "cash-multiple", route: "/merchants/buy-usdt" },
    { label: "Digital Assets", icon: "wallet-outline", route: "/merchants/digital" },
  ];

  type DrawerLink = { label: string; icon: string; route: string };
  const liquidity = lpCta({ isPm: pm.isPm, kind: pm.kind, lpStatus: pm.lpStatus, liquidityAvailable: pm.liquidityAvailable, loading: pm.loading });
  // Rows that live outside this drawer, each placed after the screen it is
  // keyed by. A merchant already is one, so "Become Merchant" is for everyone else.
  const linksAfter: Record<string, DrawerLink[]> = {
    profile: [{ label: "Accept Payment with TSA Pay", icon: "credit-card-outline", route: TSA_PAY_APPLY }],
    settings: isMerchant ? [] : [{ label: "Become Merchant", icon: "storefront-outline", route: "/merchants/merchant-request" }],
    categories: liquidity ? [{ label: liquidity.title, icon: "cash-plus", route: liquidity.route }] : [],
  };

  // One row for every link (these and the MERCHANT section), so they can't drift.
  const LinkItem = ({ item, navigation }: { item: DrawerLink; navigation: any }) => (
    <DrawerItem
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
        navigation.closeDrawer();
        router.push(item.route as any);
      }}
    />
  );

  // One drawer screen row — what react-navigation's DrawerItemList renders per
  // route, against the FULL drawer state (focus and colours come from the
  // screen actually focused), so the rows can be interleaved with LinkItems.
  const ScreenItem = ({ route, index, drawer }: { route: any; index: number; drawer: any }) => {
    const { state, navigation, descriptors } = drawer;
    const focused = index === state.index;
    const focusedOptions = descriptors[state.routes[state.index].key].options;
    const { title, drawerLabel, drawerIcon, drawerLabelStyle: labelStyle, drawerItemStyle } =
      descriptors[route.key].options;
    return (
      <DrawerItem
        route={route}
        label={drawerLabel !== undefined ? drawerLabel : title !== undefined ? title : route.name}
        icon={drawerIcon}
        focused={focused}
        activeTintColor={focusedOptions.drawerActiveTintColor}
        inactiveTintColor={focusedOptions.drawerInactiveTintColor}
        activeBackgroundColor={focusedOptions.drawerActiveBackgroundColor}
        inactiveBackgroundColor={focusedOptions.drawerInactiveBackgroundColor}
        labelStyle={labelStyle}
        style={drawerItemStyle}
        onPress={() => {
          const event = navigation.emit({ type: "drawerItemPress", target: route.key, canPreventDefault: true });
          if (!event.defaultPrevented) {
            navigation.dispatch({
              ...(focused ? DrawerActions.closeDrawer() : CommonActions.navigate(route)),
              target: state.key,
            });
          }
        }}
      />
    );
  };

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

      {drawerSegments(props.state.routes, Object.keys(linksAfter)).map((seg) => (
        <React.Fragment key={seg.anchor ?? "rest"}>
          {seg.routes.map(({ route, index }) => (
            <ScreenItem key={route.key} route={route} index={index} drawer={props} />
          ))}
          {(seg.anchor ? linksAfter[seg.anchor] : []).map((item) => (
            <LinkItem key={item.route} item={item} navigation={props.navigation} />
          ))}
        </React.Fragment>
      ))}

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
            <LinkItem key={item.route} item={item} navigation={props.navigation} />
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
                {/* Home only: the marketplace draws its own cart on the page. */}
                {isHomeTab((route as { state?: NestedNavState }).state) && (
                  <CartIconBadge color={colors.textPrimary} size={24} />
                )}
                <NotificationBell />
                <Pressable
                  onPress={() => handleAvatarPress(navigation)}
                  accessibilityRole="button"
                  accessibilityLabel="Open menu"
                  hitSlop={6}
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
                        marginRight: spacing.sm,
                      }}
                    >
                      <Text variant="bodyMed" weight="700" color="accent">
                        ...
                      </Text>
                    </View>
                  ) : currentUser && currentUser.profilePicture ? (
                    // User has profile picture
                    <View style={{ marginRight: spacing.sm }}>
                      <Avatar uri={currentUser.profilePicture} size={36} />
                    </View>
                  ) : currentUser && currentUser.name ? (
                    // User has name but no profile picture
                    <View style={{ marginRight: spacing.sm }}>
                      <Avatar name={currentUser.name} size={36} />
                    </View>
                  ) : (
                    // No user data yet: the same button, a placeholder face.
                    <View style={{ marginRight: spacing.sm }}>
                      <Avatar name="?" size={36} />
                    </View>
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
              // No ☰: the avatar on the right opens the drawer.
              headerLeft: () => null,
              headerTitle: "TSA Connect",
              title: "Dashboard",
              headerTitleAlign: "left",
              // Left-aligned with nothing before it, so the title takes the
              // screen's own gutter; the right stays tight for cart · bell · avatar.
              headerTitleContainerStyle: { marginLeft: spacing.lg, marginRight: spacing.xs },
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
            title: "Rewards / Promotion Arena",
            drawerActiveTintColor: colors.accent,
          }}
        />
      </Drawer>
    </>
  );
};

export default Layout;
