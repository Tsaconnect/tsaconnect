import { Image, Pressable, StyleSheet, Text, View } from "react-native";
import React, { useEffect, useState } from "react";
import { Drawer } from "expo-router/drawer";
import {
  DrawerContentScrollView,
  DrawerItemList,
  DrawerItem,
} from "@react-navigation/drawer";
import { COLORS } from "../../constants";
import Icon from "react-native-vector-icons/MaterialCommunityIcons";
import { router } from "expo-router";
import { Avatar } from "react-native-elements";
import api from "@/components/services/api";
import AsyncStorage from "@react-native-async-storage/async-storage";
import NotificationBell from '../../components/NotificationBell';

interface UserProfile {
  _id: string;
  name: string;
  email: string;
  username?: string;
  profilePicture?: string;
  phoneNumber?: string;
}

const Layout = () => {
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

  const CustomDrawerContent = (props: any) => (
    <DrawerContentScrollView {...props}>
      <DrawerItemList {...props} />
      {isMerchant && (
        <View style={styles.merchantSection}>
          <View style={styles.merchantDivider} />
          <Text style={styles.merchantSectionTitle}>Merchant</Text>
          <DrawerItem
            label="Merchant Dashboard"
            icon={({ color, size }) => (
              <Icon name="view-dashboard-outline" size={25} color={color} style={styles.merchantIcon} />
            )}
            activeTintColor={COLORS.primary}
            labelStyle={styles.merchantDrawerLabel}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/dashboard");
            }}
          />
          <DrawerItem
            label="Products & Services"
            icon={({ color, size }) => (
              <Icon name="shopping-outline" size={25} color={color} style={styles.merchantIcon} />
            )}
            activeTintColor={COLORS.primary}
            labelStyle={styles.merchantDrawerLabel}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/inventory");
            }}
          />
          <DrawerItem
            label="Add Product"
            icon={({ color, size }) => (
              <Icon name="plus-circle-outline" size={25} color={color} style={styles.merchantIcon} />
            )}
            activeTintColor={COLORS.primary}
            labelStyle={styles.merchantDrawerLabel}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/inventory/add");
            }}
          />
          <DrawerItem
            label="Sell USDT for Fiat"
            icon={({ color, size }) => (
              <Icon name="trending-up" size={25} color={color} style={styles.merchantIcon} />
            )}
            activeTintColor={COLORS.primary}
            labelStyle={styles.merchantDrawerLabel}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/sell-usdt");
            }}
          />
          <DrawerItem
            label="Buy Crypto with Cash"
            icon={({ color, size }) => (
              <Icon name="cash-multiple" size={25} color={color} style={styles.merchantIcon} />
            )}
            activeTintColor={COLORS.primary}
            labelStyle={styles.merchantDrawerLabel}
            onPress={() => {
              props.navigation.closeDrawer();
              router.push("/merchants/buy-usdt");
            }}
          />
          <DrawerItem
            label="Digital Assets"
            icon={({ color, size }) => (
              <Icon name="wallet-outline" size={25} color={color} style={styles.merchantIcon} />
            )}
            activeTintColor={COLORS.primary}
            labelStyle={styles.merchantDrawerLabel}
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
                    marginRight: 15,
                    marginLeft: -10,
                  }}
                />
              );
            },
            headerTintColor: navigation.isFocused() ? COLORS.primary : "black",
            drawerLabelStyle: {
              marginHorizontal: -20,
              fontSize: 16,
              padding: -20,
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
                  style={styles.avatarContainer}
                >
                  {loading ? (
                    // Loading state
                    <View style={styles.avatarPlaceholder}>
                      <Text style={styles.avatarLoadingText}>...</Text>
                    </View>
                  ) : currentUser && currentUser.profilePicture ? (
                    // User has profile picture
                    <Avatar
                      rounded
                      source={{ uri: currentUser.profilePicture }}
                      size="small"
                      containerStyle={styles.avatar}
                      onPress={handleAvatarPress}
                    />
                  ) : currentUser && currentUser.name ? (
                    // User has name but no profile picture
                    <Avatar
                      rounded
                      title={getUserInitials(currentUser.name)}
                      size="small"
                      overlayContainerStyle={{ backgroundColor: COLORS.primary }}
                      containerStyle={styles.avatar}
                      onPress={handleAvatarPress}
                    />
                  ) : (
                    // No user data (guest or not logged in)
                    <Avatar
                      rounded
                      icon={{ name: "person", type: "material" }}
                      size="small"
                      overlayContainerStyle={{ backgroundColor: "#9D6B38" }}
                      containerStyle={styles.avatar}
                      onPress={() => {
                        if (!isAuthenticated) {
                          router.push("/login");
                        } else {
                          router.push("/profile");
                        }
                      }}
                    />
                  )}
                </Pressable>
              </View>
              ),
              headerTitle: "TSA Connect",
              title: "Dashboard",
              headerTitleAlign: "center",
              headerTitleStyle: {
                fontSize: 16,
                color: navigation.isFocused() ? COLORS.primary : "black",
              },
              headerTintColor: navigation.isFocused()
                ? COLORS.primary
                : "black",
              drawerActiveTintColor: COLORS.primary,
            })
          }
        />
        <Drawer.Screen
          name="orderlist"
          options={{
            title: "My Orders",
            drawerActiveTintColor: COLORS.primary,
          }}
        />
        <Drawer.Screen
          name="myAdverts"
          options={{
            title: "My Adverts",
            drawerActiveTintColor: COLORS.primary,
          }}
        />
        <Drawer.Screen
          name="profile"
          options={{
            title: "Profile",
            drawerActiveTintColor: COLORS.primary,
            // Protect profile route if not authenticated
            ...(!isAuthenticated && {
              drawerItemStyle: { display: 'none' }
            }),
          }}
        />
        <Drawer.Screen
          name="settings"
          options={{
            title: "Settings",
            drawerActiveTintColor: COLORS.primary,
          }}
        />
        <Drawer.Screen
          name="categories"
          options={{
            title: "Category",
            drawerActiveTintColor: COLORS.primary,
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

const styles = StyleSheet.create({
  avatar: {
    marginRight: 10,
  },
  avatarContainer: {
    flexDirection: "row",
    alignItems: "center",
  },
  avatarPlaceholder: {
    width: 40,
    height: 40,
    borderRadius: 20,
    backgroundColor: '#f0f0f0',
    justifyContent: 'center',
    alignItems: 'center',
    marginRight: 10,
  },
  avatarLoadingText: {
    fontSize: 18,
    color: COLORS.primary,
    fontWeight: 'bold',
  },
  merchantSection: {
    marginTop: 5,
  },
  merchantDivider: {
    height: 1,
    backgroundColor: COLORS.lightGray || '#e0e0e0',
    marginHorizontal: 20,
    marginBottom: 8,
  },
  merchantSectionTitle: {
    fontSize: 13,
    fontWeight: '600',
    color: COLORS.gray || '#999',
    paddingHorizontal: 20,
    marginBottom: 4,
    textTransform: 'uppercase',
    letterSpacing: 0.5,
  },
  merchantDrawerLabel: {
    marginHorizontal: -20,
    fontSize: 16,
  },
  merchantIcon: {
    marginVertical: -5,
    marginRight: 15,
    marginLeft: -10,
  },
});