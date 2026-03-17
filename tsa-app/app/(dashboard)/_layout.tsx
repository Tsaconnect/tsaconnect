import { Image, Pressable, StyleSheet, Text, View } from "react-native";
import React, { useEffect, useState } from "react";
import { Drawer } from "expo-router/drawer";
import { COLORS } from "../../constants";
import Icon from "react-native-vector-icons/MaterialCommunityIcons";
import { router } from "expo-router";
import { Avatar } from "react-native-elements";
import api from "@/components/services/api";
//import { api } from "../../services/api"; // Direct import of API service

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

  // Handle avatar press - refresh profile and navigate
  const handleAvatarPress = async () => {
    try {
      // Refresh profile data before navigating
      await refreshProfile();
      router.push("/profile");
    } catch (error) {
      console.error('Error navigating to profile:', error);
      router.push("/profile");
    }
  };

  // Get user initials for avatar fallback
  const getUserInitials = (name: string) => {
    if (!name) return '?';
    const names = name.split(' ');
    if (names.length === 1) return name.charAt(0).toUpperCase();
    return (names[0].charAt(0) + names[names.length - 1].charAt(0)).toUpperCase();
  };

  return (
    <>
      <Drawer
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
});