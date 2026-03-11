import api from '@/components/services/api';
import { router } from 'expo-router';
import React, { useEffect, useState } from 'react';
import {
    View,
    Text,
    StyleSheet,

    Alert,
    Pressable
} from 'react-native';
import * as Clipboard from 'expo-clipboard'; // Updated import
import { SafeAreaView } from "react-native-safe-area-context"
const MerchantProfileScreen = () => {
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

    const copyToClipboard = async () => { // Made async
        if (!user?.referralCode) {
            Alert.alert("Error", "No referral code available");
            return;
        }

        try {
            await Clipboard.setStringAsync(user.referralCode);
            Alert.alert("Copied to clipboard", "Referral code copied to clipboard!");
        } catch (error) {
            console.error("Copy failed:", error);
            Alert.alert("Error", "Failed to copy to clipboard");
        }
    };

    const handleVerifyEmail = async () => {
        try {
            if (!token) {
                Alert.alert("Error", "Not authenticated");
                return;
            }
            const result = await api.sendVerificationEmail();
            if (result.success) {
                router.push("/profile/verify-email");
                return;
            }

        } catch (error: any) {
            console.log(error?.response?.data?.message);
            Alert.alert("An error occurred while sending verification email.");
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
        <SafeAreaView style={styles.container}>
            <View style={styles.header}>
                <View style={styles.profileImageContainer}>
                    <View style={styles.placeholderImage}>
                        <Text style={styles.placeholderText}>{getUserInitials(user?.name)}</Text>
                    </View>
                </View>
                <Text style={styles.userName}>{user?.name}</Text>
                <Text style={styles.userRole}>{user?.role.toUpperCase()}</Text>
            </View>

            <View style={styles.section}>
                <Text style={styles.sectionTitle}>Account Settings</Text>
                <Pressable style={styles.menuItem}>
                    <Text style={styles.menuItemText}>Edit Profile</Text>
                </Pressable>
                <Pressable style={styles.menuItem}>
                    <Text style={styles.menuItemText}>Security & Privacy</Text>
                </Pressable>
                <Pressable style={styles.menuItem}>
                    <Text style={styles.menuItemText}>Notifications</Text>
                </Pressable>
                {/* Optional: Add referral code section if needed */}
                {user?.referralCode && (
                    <Pressable
                        style={styles.menuItem}
                        onPress={copyToClipboard}
                    >
                        <Text style={styles.menuItemText}>
                            Copy Referral Code: {user.referralCode}
                        </Text>
                    </Pressable>
                )}
            </View>

            <Pressable onPress={handleLogout} style={styles.logoutButton}>
                <Text style={styles.logoutButtonText}>Log Out</Text>
            </Pressable>
        </SafeAreaView>
    );
};

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: '#F9FAFB',
    },
    header: {
        alignItems: 'center',
        paddingVertical: 40,
        backgroundColor: '#FFFFFF',
        borderBottomWidth: 1,
        borderBottomColor: '#E5E7EB',
    },
    profileImageContainer: {
        marginBottom: 16,
    },
    placeholderImage: {
        width: 100,
        height: 100,
        borderRadius: 50,
        backgroundColor: '#3B82F6',
        justifyContent: 'center',
        alignItems: 'center',
    },
    placeholderText: {
        color: '#FFFFFF',
        fontSize: 32,
        fontWeight: '700',
    },
    userName: {
        fontSize: 24,
        fontWeight: '600',
        color: '#111827',
    },
    userRole: {
        fontSize: 14,
        color: '#6B7280',
        marginTop: 4,
    },
    section: {
        marginTop: 24,
        paddingHorizontal: 16,
    },
    sectionTitle: {
        fontSize: 13,
        fontWeight: '600',
        color: '#9CA3AF',
        textTransform: 'uppercase',
        letterSpacing: 0.5,
        marginBottom: 12,
        marginLeft: 4,
    },
    menuItem: {
        backgroundColor: '#FFFFFF',
        paddingVertical: 16,
        paddingHorizontal: 16,
        borderRadius: 12,
        marginBottom: 8,
        borderWidth: 1,
        borderColor: '#F3F4F6',
    },
    menuItemText: {
        fontSize: 16,
        color: '#374151',
        fontWeight: '500',
    },
    logoutButton: {
        margin: 16,
        marginTop: 'auto',
        backgroundColor: '#FEE2E2',
        paddingVertical: 16,
        borderRadius: 12,
        alignItems: 'center',
    },
    logoutButtonText: {
        color: '#DC2626',
        fontSize: 16,
        fontWeight: '600',
    },
});

export default MerchantProfileScreen;