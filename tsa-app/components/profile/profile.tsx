import React, { useEffect, useState } from "react";
import {
  StyleSheet,
  View,
  Text,
  TouchableOpacity,
  ActivityIndicator,
  Alert,
  ScrollView,
} from "react-native";
import { Avatar, Icon } from "react-native-elements";
import { router } from "expo-router";
import * as Clipboard from "expo-clipboard";
import api from "../services/api";


const ProfileScreen = () => {
  const [user, setUser] = useState<any>(null);
  const [loading, setLoading] = useState(true);
  const [token, setToken] = useState<string | null>(null);

  // Get token on component mount
  useEffect(() => {
    const getToken = async () => {
      try {
        const storedToken = await api.getStoredToken();
        setToken(storedToken);
      } catch (error) {
        console.error("Error getting token:", error);
      }
    };
    getToken();
  }, []);

  async function getLoggedInUser() {
    try {
      setLoading(true);

      // Check if we have a token
      if (!token) {
        Alert.alert("Error", "Not authenticated. Please login again.");
        router.replace("/login");
        return;
      }

      // Use API service to get profile
      const response = await api.getProfile();

      if (response.success && response.data) {
        setUser(response.data);
      } else {
        Alert.alert("Error", response.message || "Failed to load user data");
      }
    } catch (error: any) {
      console.error("Error fetching user:", error);
      Alert.alert(
        "Error",
        api.getErrorMessage(error) || "Failed to load profile"
      );
    } finally {
      setLoading(false);
    }
  }

  useEffect(() => {
    if (token) {
      getLoggedInUser();
    }
  }, [token]);

  const copyToClipboard = () => {
    if (!user?.referralCode) {
      Alert.alert("Error", "No referral code available");
      return;
    }

    Clipboard.setStringAsync(user.referralCode);
    Alert.alert("Copied to clipboard", "Referral code copied to clipboard!");
  };

  const handleVerifyEmail = async () => {
    try {
      if (!token) {
        Alert.alert("Error", "Not authenticated");
        return;
      }
      const result = await api.sendOtp();
      if (result.success) {
        router.push({
          pathname: "/verify",
          params: { email: user?.email },
        });
      } else {
        Alert.alert("Error", result.message || "Failed to send verification code.");
      }
    } catch (error: any) {
      Alert.alert("Error", "An error occurred while sending verification code.");
    }
  };

  const handleKycVerification = () => {
    router.push("/profile/kyc");
  };

  // Handle logout
  const handleLogout = async () => {
    try {
      await api.clearAuth();
      router.replace("/login");
    } catch (error) {
      console.error("Logout error:", error);
      Alert.alert("Error", "Failed to logout");
    }
  };

  // Get user initials for avatar fallback
  const getUserInitials = (name: string) => {
    if (!name) return '?';
    return name.charAt(0).toUpperCase();
  };

  return (
    <View style={styles.container}>
      {loading || !user ? (
        <View style={styles.loadingContainer}>
          <ActivityIndicator size={24} color="#9D6B38" />
        </View>
      ) : (
        <ScrollView showsVerticalScrollIndicator={false}>
          <View style={styles.card}>
            <TouchableOpacity
              style={styles.editIcon}
              onPress={() =>
                router.push({ pathname: "/profile/edit", params: user })
              }
            >
              <Icon name="edit" size={20} color="#9D6B38" />
            </TouchableOpacity>
            <View style={styles.profileInfo}>
              {user?.profilePicture ? (
                <Avatar
                  rounded
                  source={{ uri: user.profilePicture }}
                  size="large"
                />
              ) : (
                <Avatar
                  rounded
                  title={getUserInitials(user.name)}
                  size="large"
                  overlayContainerStyle={{ backgroundColor: "#9D6B38" }}
                  titleStyle={{ color: "#fff" }}
                />
              )}
              <View style={styles.textInfo}>
                <Text style={styles.fullName}>{user.name}</Text>
                <Text style={styles.phoneNumber}>{user?.phoneNumber}</Text>
              </View>
            </View>

            {!user.emailVerified ? (
              <TouchableOpacity
                style={styles.verificationButton}
                onPress={handleVerifyEmail}
              >
                <Icon name="mail" size={20} color="#FFFFFF" />
                <Text style={styles.verificationText}>Verify Email</Text>
              </TouchableOpacity>
            ) : user.verificationStatus !== 'verified' ? (
              <TouchableOpacity
                style={styles.verificationButton}
                onPress={handleKycVerification}
              >
                <Icon name="account-box" size={20} color="#FFFFFF" />
                <Text style={styles.verificationText}>
                  Complete KYC Verification
                </Text>
              </TouchableOpacity>
            ) : (
              <View style={styles.verifiedContainer}>
                <Icon name="verified" size={20} color="green" />
                <Text style={styles.verifiedText}>Verified</Text>
              </View>
            )}
          </View>
          <View style={styles.linksContainer}>
            <TouchableOpacity
              style={styles.profileLink}
              onPress={() => router.push("/myAdverts")}
            >
              <Icon name="description" size={20} color="#9D6B38" />
              <Text style={styles.linkText}>My Adverts</Text>
            </TouchableOpacity>
            <TouchableOpacity
              style={styles.profileLink}
              onPress={() => router.push("/orderlist")}
            >
              <Icon name="list" size={20} color="#9D6B38" />
              <Text style={styles.linkText}>Orders</Text>
            </TouchableOpacity>
            <TouchableOpacity style={styles.profileLink}>
              <Icon name="favorite" size={20} color="#9D6B38" />
              <Text style={styles.linkText}>Wishlist</Text>
            </TouchableOpacity>
            <TouchableOpacity style={styles.profileLink}>
              <Icon name="account-balance-wallet" size={20} color="#9D6B38" />
              <View style={styles.balanceRow}>
                <Text style={styles.balance}>{user?.balance || "0"} USD</Text>
                <Text style={styles.balanceText}>My Balance</Text>
              </View>
            </TouchableOpacity>
            <TouchableOpacity style={styles.profileLink}>
              <Icon name="share" size={20} color="#9D6B38" />
              <View style={styles.referralRow}>
                <Text style={styles.referralCode}>{user?.referralCode || "N/A"}</Text>
                <TouchableOpacity
                  style={styles.copyButton}
                  onPress={copyToClipboard}
                >
                  <Text style={styles.copyButtonText}>Copy</Text>
                </TouchableOpacity>
              </View>
            </TouchableOpacity>

            <TouchableOpacity
              style={styles.profileLink}
              onPress={() => router.push("/(dashboard)/settings")}
            >
              <Icon name="settings" size={20} color="#9D6B38" />
              <Text style={styles.linkText}>Settings</Text>
            </TouchableOpacity>

            <TouchableOpacity style={styles.profileLink} onPress={handleLogout}>
              <Icon name="exit-to-app" size={20} color="#9D6B38" />
              <Text style={styles.linkText}>Logout</Text>
            </TouchableOpacity>
          </View>
        </ScrollView>
      )}
    </View>
  );
};

const styles = StyleSheet.create({
  container: {
    flex: 1,
    backgroundColor: "#f0f0f0",
    padding: 10,
  },
  loadingContainer: {
    flex: 1,
    justifyContent: "center",
    alignItems: "center",
  },
  card: {
    borderRadius: 10,
    padding: 20,
    width: "94%",
    height: 217,
    backgroundColor: "#F5FCFF",
  },
  editIcon: {
    position: "absolute",
    top: 10,
    right: 10,
  },
  profileInfo: {
    alignItems: "center",
    justifyContent: "center",
    marginTop: 15,
  },
  textInfo: {
    marginLeft: 15,
  },
  fullName: {
    fontSize: 18,
    fontWeight: "bold",
  },
  phoneNumber: {
    fontSize: 14,
    color: "#666",
  },
  linksContainer: {
    marginTop: 20,
    minHeight: 300,
  },
  profileLink: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "flex-start",
    padding: 20,
    borderBottomWidth: 1,
    borderBottomColor: "#ccc",
    borderRadius: 10,
  },
  linkText: {
    marginLeft: 10,
    fontSize: 16,
  },
  balance: {
    marginTop: 5,
    fontSize: 16,
    fontWeight: "bold",
  },
  balanceRow: {
    marginLeft: 15,
  },
  balanceText: {
    fontSize: 14,
  },
  referralCode: {
    fontSize: 16,
    marginLeft: 15,
  },
  referralRow: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "space-between",
    width: "100%",
  },
  copyButton: {
    marginLeft: 10,
    backgroundColor: "#9D6B38",
    paddingVertical: 5,
    paddingHorizontal: 10,
    borderRadius: 5,
    marginRight: 10,
  },
  copyButtonText: {
    color: "#fff",
    fontSize: 14,
  },
  verificationButton: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    backgroundColor: "#9D6B38",
    padding: 15,
    borderRadius: 10,
    marginVertical: 10,
  },
  verificationText: {
    color: "#fff",
    marginLeft: 10,
    fontSize: 16,
  },
  verifiedContainer: {
    flexDirection: "row",
    alignItems: "center",
    justifyContent: "center",
    padding: 15,
    borderRadius: 10,
    marginVertical: 10,
    backgroundColor: "#e0ffe0",
  },
  verifiedText: {
    color: "green",
    marginLeft: 10,
    fontSize: 16,
  },
});

export default ProfileScreen;