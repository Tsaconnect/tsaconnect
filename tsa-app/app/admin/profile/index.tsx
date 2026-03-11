// app/admin/profile/index.tsx
import api from '@/components/services/api';
import { router } from 'expo-router';
import React, { useEffect, useState } from 'react';
import {
    View,
    Text,
    StyleSheet,
    Alert,
    Pressable,
    ScrollView,
    ActivityIndicator,
    StatusBar,
    Dimensions,
} from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import { SafeAreaView } from 'react-native-safe-area-context';
const { width } = Dimensions.get('window');

const AdminProfileScreen = () => {
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

            if (!token) {
                Alert.alert("Error", "Not authenticated. Please login again.");
                router.replace("/login");
                return;
            }

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

    const copyToClipboard = async (text: string, message: string) => {
        try {
            await Clipboard.setStringAsync(text);
            Alert.alert("Success", message);
        } catch (error) {
            console.error("Failed to copy to clipboard:", error);
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

    const handleLogout = async () => {
        Alert.alert(
            "Logout",
            "Are you sure you want to logout?",
            [
                {
                    text: "Cancel",
                    style: "cancel"
                },
                {
                    text: "Logout",
                    style: "destructive",
                    onPress: async () => {
                        try {
                            await api.clearAuth();
                            router.replace("/login");
                        } catch (error) {
                            console.error("Logout error:", error);
                            Alert.alert("Error", "Failed to logout");
                        }
                    }
                }
            ]
        );
    };

    const handleSwitchToMarketplace = () => {
        Alert.alert(
            "Switch Interface",
            "Switch to marketplace view?",
            [
                {
                    text: "Cancel",
                    style: "cancel"
                },
                {
                    text: "Switch",
                    onPress: () => router.replace('/home')
                }
            ]
        );
    };

    // Get user initials for avatar
    const getUserInitials = () => {
        if (user?.fullName) {
            return user.fullName.split(' ').map((n: string) => n[0]).join('').toUpperCase().substring(0, 2);
        }
        if (user?.email) {
            return user.email.charAt(0).toUpperCase();
        }
        return 'AD';
    };

    if (loading) {
        return (
            <SafeAreaView style={styles.loadingContainer}>
                <StatusBar barStyle="dark-content" backgroundColor="#F5F5F5" />
                <ActivityIndicator size="large" color="#8B4513" />
                <Text style={styles.loadingText}>Loading profile...</Text>
            </SafeAreaView>
        );
    }

    return (
        <SafeAreaView style={styles.container}>
            <StatusBar barStyle="light-content" backgroundColor="#8B4513" />

            <ScrollView
                showsVerticalScrollIndicator={false}
                contentContainerStyle={styles.scrollContent}
            >
                {/* Header with Gradient */}
                <LinearGradient
                    colors={['#8B4513', '#A0522D', '#CD853F']}
                    start={{ x: 0, y: 0 }}
                    end={{ x: 1, y: 1 }}
                    style={styles.header}
                >
                    {/* Decorative Circles */}
                    <View style={styles.decorativeCircle1} />
                    <View style={styles.decorativeCircle2} />

                    <View style={styles.headerContent}>
                        {/* Back Button */}
                        <Pressable
                            style={styles.backButton}
                            onPress={() => router.back()}
                        >
                            <Ionicons name="arrow-back" size={24} color="#FFF" />
                        </Pressable>

                        {/* Profile Image */}
                        <View style={styles.profileImageContainer}>
                            <LinearGradient
                                colors={['#FFF', '#F5F5F5']}
                                style={styles.profileImageBorder}
                            >
                                <View style={styles.profileImage}>
                                    <Text style={styles.profileImageText}>
                                        {getUserInitials()}
                                    </Text>
                                </View>
                            </LinearGradient>

                            {/* Admin Badge */}
                            <View style={styles.adminBadge}>
                                <Ionicons name="shield" size={16} color="#8B4513" />
                            </View>
                        </View>

                        {/* User Info */}
                        <Text style={styles.userName}>
                            {user?.fullName || 'Administrator'}
                        </Text>
                        <Text style={styles.userRole}>System Administrator</Text>

                        {/* Email with verification status */}
                        <View style={styles.emailContainer}>
                            <Ionicons name="mail-outline" size={16} color="rgba(255,255,255,0.8)" />
                            <Text style={styles.userEmail} numberOfLines={1}>
                                {user?.email || 'admin@tsa.com'}
                            </Text>
                            {user?.isEmailVerified ? (
                                <View style={styles.verifiedBadge}>
                                    <Ionicons name="checkmark-circle" size={16} color="#4CAF50" />
                                    <Text style={styles.verifiedText}>Verified</Text>
                                </View>
                            ) : (
                                <Pressable onPress={handleVerifyEmail} style={styles.verifyButton}>
                                    <Text style={styles.verifyText}>Verify</Text>
                                </Pressable>
                            )}
                        </View>
                    </View>
                </LinearGradient>

                {/* Stats Cards */}
                <View style={styles.statsContainer}>
                    <LinearGradient
                        colors={['#FFFFFF', '#F9F9F9']}
                        style={styles.statCard}
                    >
                        <Ionicons name="people-outline" size={24} color="#8B4513" />
                        <Text style={styles.statValue}>1,234</Text>
                        <Text style={styles.statLabel}>Users</Text>
                    </LinearGradient>

                    <LinearGradient
                        colors={['#FFFFFF', '#F9F9F9']}
                        style={styles.statCard}
                    >
                        <Ionicons name="cube-outline" size={24} color="#8B4513" />
                        <Text style={styles.statValue}>567</Text>
                        <Text style={styles.statLabel}>Products</Text>
                    </LinearGradient>

                    <LinearGradient
                        colors={['#FFFFFF', '#F9F9F9']}
                        style={styles.statCard}
                    >
                        <Ionicons name="storefront-outline" size={24} color="#8B4513" />
                        <Text style={styles.statValue}>89</Text>
                        <Text style={styles.statLabel}>Merchants</Text>
                    </LinearGradient>
                </View>

                {/* Account Settings Section */}
                <View style={styles.section}>
                    <Text style={styles.sectionTitle}>Account Settings</Text>

                    <Pressable style={styles.menuItem}>
                        <LinearGradient
                            colors={['#FFFFFF', '#F9F9F9']}
                            style={styles.menuItemGradient}
                        >
                            <View style={styles.menuItemLeft}>
                                <View style={[styles.menuIcon, { backgroundColor: 'rgba(139, 69, 19, 0.1)' }]}>
                                    <Ionicons name="person-outline" size={20} color="#8B4513" />
                                </View>
                                <Text style={styles.menuItemText}>Edit Profile</Text>
                            </View>
                            <Ionicons name="chevron-forward" size={20} color="#8B4513" />
                        </LinearGradient>
                    </Pressable>

                    <Pressable style={styles.menuItem}>
                        <LinearGradient
                            colors={['#FFFFFF', '#F9F9F9']}
                            style={styles.menuItemGradient}
                        >
                            <View style={styles.menuItemLeft}>
                                <View style={[styles.menuIcon, { backgroundColor: 'rgba(139, 69, 19, 0.1)' }]}>
                                    <Ionicons name="shield-outline" size={20} color="#8B4513" />
                                </View>
                                <Text style={styles.menuItemText}>Security & Privacy</Text>
                            </View>
                            <Ionicons name="chevron-forward" size={20} color="#8B4513" />
                        </LinearGradient>
                    </Pressable>

                    <Pressable style={styles.menuItem}>
                        <LinearGradient
                            colors={['#FFFFFF', '#F9F9F9']}
                            style={styles.menuItemGradient}
                        >
                            <View style={styles.menuItemLeft}>
                                <View style={[styles.menuIcon, { backgroundColor: 'rgba(139, 69, 19, 0.1)' }]}>
                                    <Ionicons name="notifications-outline" size={20} color="#8B4513" />
                                </View>
                                <Text style={styles.menuItemText}>Notifications</Text>
                            </View>
                            <Ionicons name="chevron-forward" size={20} color="#8B4513" />
                        </LinearGradient>
                    </Pressable>
                </View>

                {/* Admin Tools Section */}
                <View style={styles.section}>
                    <Text style={styles.sectionTitle}>Admin Tools</Text>

                    <Pressable style={styles.menuItem}>
                        <LinearGradient
                            colors={['#FFFFFF', '#F9F9F9']}
                            style={styles.menuItemGradient}
                        >
                            <View style={styles.menuItemLeft}>
                                <View style={[styles.menuIcon, { backgroundColor: 'rgba(139, 69, 19, 0.1)' }]}>
                                    <Ionicons name="people" size={20} color="#8B4513" />
                                </View>
                                <Text style={styles.menuItemText}>User Management</Text>
                            </View>
                            <View style={styles.badge}>
                                <Text style={styles.badgeText}>12</Text>
                            </View>
                        </LinearGradient>
                    </Pressable>

                    <Pressable style={styles.menuItem}>
                        <LinearGradient
                            colors={['#FFFFFF', '#F9F9F9']}
                            style={styles.menuItemGradient}
                        >
                            <View style={styles.menuItemLeft}>
                                <View style={[styles.menuIcon, { backgroundColor: 'rgba(139, 69, 19, 0.1)' }]}>
                                    <Ionicons name="stats-chart" size={20} color="#8B4513" />
                                </View>
                                <Text style={styles.menuItemText}>Analytics</Text>
                            </View>
                            <Ionicons name="chevron-forward" size={20} color="#8B4513" />
                        </LinearGradient>
                    </Pressable>

                    <Pressable style={styles.menuItem}>
                        <LinearGradient
                            colors={['#FFFFFF', '#F9F9F9']}
                            style={styles.menuItemGradient}
                        >
                            <View style={styles.menuItemLeft}>
                                <View style={[styles.menuIcon, { backgroundColor: 'rgba(139, 69, 19, 0.1)' }]}>
                                    <Ionicons name="settings-outline" size={20} color="#8B4513" />
                                </View>
                                <Text style={styles.menuItemText}>System Settings</Text>
                            </View>
                            <Ionicons name="chevron-forward" size={20} color="#8B4513" />
                        </LinearGradient>
                    </Pressable>
                </View>

                {/* Referral Code Section */}
                {user?.referralCode && (
                    <View style={styles.section}>
                        <Text style={styles.sectionTitle}>Referral Program</Text>
                        <Pressable
                            style={styles.referralCard}
                            onPress={() => copyToClipboard(user.referralCode, "Referral code copied!")}
                        >
                            <LinearGradient
                                colors={['#8B4513', '#A0522D']}
                                start={{ x: 0, y: 0 }}
                                end={{ x: 1, y: 1 }}
                                style={styles.referralGradient}
                            >
                                <View style={styles.referralContent}>
                                    <View style={styles.referralLeft}>
                                        <Ionicons name="gift-outline" size={24} color="#FFF" />
                                        <View>
                                            <Text style={styles.referralTitle}>Your Referral Code</Text>
                                            <Text style={styles.referralCode}>{user.referralCode}</Text>
                                        </View>
                                    </View>
                                    <View style={styles.referralRight}>
                                        <Text style={styles.tapToCopy}>Tap to copy</Text>
                                        <Ionicons name="copy-outline" size={20} color="#FFF" />
                                    </View>
                                </View>
                            </LinearGradient>
                        </Pressable>
                    </View>
                )}

                {/* Switch Interface */}
                <Pressable
                    style={styles.switchButton}
                    onPress={handleSwitchToMarketplace}
                >
                    <LinearGradient
                        colors={['#8B4513', '#A0522D']}
                        start={{ x: 0, y: 0 }}
                        end={{ x: 1, y: 0 }}
                        style={styles.switchGradient}
                    >
                        <Ionicons name="swap-horizontal" size={24} color="#FFF" />
                        <View style={styles.switchTextContainer}>
                            <Text style={styles.switchLabel}>Switch to</Text>
                            <Text style={styles.switchTitle}>Marketplace View</Text>
                        </View>
                        <Ionicons name="arrow-forward" size={24} color="#FFF" />
                    </LinearGradient>
                </Pressable>

                {/* Logout Button */}
                <Pressable onPress={handleLogout} style={styles.logoutButton}>
                    <LinearGradient
                        colors={['#FFF', '#FEE2E2']}
                        style={styles.logoutGradient}
                    >
                        <Ionicons name="log-out-outline" size={20} color="#DC2626" />
                        <Text style={styles.logoutButtonText}>Log Out</Text>
                    </LinearGradient>
                </Pressable>

                {/* Version Info */}
                <Text style={styles.versionText}>Version 2.0.0 • TSA Admin</Text>
            </ScrollView>
        </SafeAreaView>
    );
};

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: '#F5F5F5',
    },
    loadingContainer: {
        flex: 1,
        justifyContent: 'center',
        alignItems: 'center',
        backgroundColor: '#F5F5F5',
    },
    loadingText: {
        marginTop: 12,
        fontSize: 14,
        color: '#666',
    },
    scrollContent: {
        flexGrow: 1,
        paddingBottom: 20,
    },
    header: {
        paddingTop: 50,
        paddingBottom: 40,
        borderBottomLeftRadius: 30,
        borderBottomRightRadius: 30,
        position: 'relative',
        overflow: 'hidden',
    },
    decorativeCircle1: {
        position: 'absolute',
        top: -50,
        right: -50,
        width: 150,
        height: 150,
        borderRadius: 75,
        backgroundColor: 'rgba(255,255,255,0.1)',
    },
    decorativeCircle2: {
        position: 'absolute',
        bottom: -30,
        left: -30,
        width: 100,
        height: 100,
        borderRadius: 50,
        backgroundColor: 'rgba(255,255,255,0.1)',
    },
    headerContent: {
        alignItems: 'center',
        paddingHorizontal: 20,
    },
    backButton: {
        position: 'absolute',
        top: -30,
        left: 20,
        width: 40,
        height: 40,
        borderRadius: 20,
        backgroundColor: 'rgba(255,255,255,0.2)',
        justifyContent: 'center',
        alignItems: 'center',
    },
    profileImageContainer: {
        marginBottom: 16,
        position: 'relative',
    },
    profileImageBorder: {
        width: 110,
        height: 110,
        borderRadius: 55,
        padding: 3,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.2,
        shadowRadius: 8,
        elevation: 5,
    },
    profileImage: {
        width: 104,
        height: 104,
        borderRadius: 52,
        backgroundColor: '#8B4513',
        justifyContent: 'center',
        alignItems: 'center',
    },
    profileImageText: {
        color: '#FFFFFF',
        fontSize: 40,
        fontWeight: '700',
    },
    adminBadge: {
        position: 'absolute',
        bottom: 5,
        right: 5,
        backgroundColor: '#FFF',
        width: 30,
        height: 30,
        borderRadius: 15,
        justifyContent: 'center',
        alignItems: 'center',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.1,
        shadowRadius: 4,
        elevation: 3,
    },
    userName: {
        fontSize: 24,
        fontWeight: '700',
        color: '#FFFFFF',
        marginBottom: 4,
    },
    userRole: {
        fontSize: 14,
        color: 'rgba(255,255,255,0.9)',
        marginBottom: 12,
    },
    emailContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: 'rgba(255,255,255,0.2)',
        paddingHorizontal: 16,
        paddingVertical: 8,
        borderRadius: 20,
    },
    userEmail: {
        fontSize: 14,
        color: '#FFFFFF',
        marginLeft: 8,
        marginRight: 8,
        maxWidth: 180,
    },
    verifiedBadge: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: 'rgba(76, 175, 80, 0.2)',
        paddingHorizontal: 8,
        paddingVertical: 4,
        borderRadius: 12,
    },
    verifiedText: {
        fontSize: 10,
        color: '#4CAF50',
        marginLeft: 2,
    },
    verifyButton: {
        backgroundColor: '#FFF',
        paddingHorizontal: 12,
        paddingVertical: 4,
        borderRadius: 12,
    },
    verifyText: {
        fontSize: 10,
        color: '#8B4513',
        fontWeight: '600',
    },
    statsContainer: {
        flexDirection: 'row',
        justifyContent: 'space-around',
        paddingHorizontal: 20,
        marginTop: -25,
        marginBottom: 20,
    },
    statCard: {
        width: (width - 60) / 3,
        backgroundColor: '#FFFFFF',
        borderRadius: 12,
        padding: 12,
        alignItems: 'center',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    statValue: {
        fontSize: 16,
        fontWeight: '700',
        color: '#333',
        marginTop: 4,
    },
    statLabel: {
        fontSize: 10,
        color: '#666',
        marginTop: 2,
    },
    section: {
        paddingHorizontal: 20,
        marginBottom: 24,
    },
    sectionTitle: {
        fontSize: 13,
        fontWeight: '600',
        color: '#8B4513',
        textTransform: 'uppercase',
        letterSpacing: 0.5,
        marginBottom: 12,
        marginLeft: 4,
    },
    menuItem: {
        marginBottom: 8,
        borderRadius: 12,
        overflow: 'hidden',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    menuItemGradient: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'space-between',
        paddingVertical: 14,
        paddingHorizontal: 16,
        backgroundColor: '#FFFFFF',
    },
    menuItemLeft: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    menuIcon: {
        width: 36,
        height: 36,
        borderRadius: 18,
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 12,
    },
    menuItemText: {
        fontSize: 15,
        color: '#333',
        fontWeight: '500',
    },
    badge: {
        backgroundColor: '#8B4513',
        paddingHorizontal: 8,
        paddingVertical: 4,
        borderRadius: 12,
    },
    badgeText: {
        color: '#FFFFFF',
        fontSize: 10,
        fontWeight: '600',
    },
    referralCard: {
        borderRadius: 12,
        overflow: 'hidden',
        shadowColor: '#8B4513',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.2,
        shadowRadius: 8,
        elevation: 5,
    },
    referralGradient: {
        padding: 16,
    },
    referralContent: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
    },
    referralLeft: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    referralTitle: {
        fontSize: 12,
        color: 'rgba(255,255,255,0.9)',
        marginLeft: 12,
    },
    referralCode: {
        fontSize: 20,
        fontWeight: '700',
        color: '#FFFFFF',
        marginLeft: 12,
    },
    referralRight: {
        alignItems: 'flex-end',
    },
    tapToCopy: {
        fontSize: 10,
        color: 'rgba(255,255,255,0.7)',
        marginBottom: 4,
    },
    switchButton: {
        marginHorizontal: 20,
        marginBottom: 16,
        borderRadius: 12,
        overflow: 'hidden',
        shadowColor: '#8B4513',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.2,
        shadowRadius: 8,
        elevation: 5,
    },
    switchGradient: {
        flexDirection: 'row',
        alignItems: 'center',
        paddingVertical: 16,
        paddingHorizontal: 20,
    },
    switchTextContainer: {
        flex: 1,
        marginLeft: 12,
    },
    switchLabel: {
        fontSize: 11,
        color: 'rgba(255,255,255,0.8)',
    },
    switchTitle: {
        fontSize: 16,
        fontWeight: '700',
        color: '#FFFFFF',
    },
    logoutButton: {
        marginHorizontal: 20,
        marginBottom: 12,
        borderRadius: 12,
        overflow: 'hidden',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    logoutGradient: {
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        paddingVertical: 16,
        backgroundColor: '#FFF',
    },
    logoutButtonText: {
        color: '#DC2626',
        fontSize: 16,
        fontWeight: '600',
        marginLeft: 8,
    },
    versionText: {
        fontSize: 11,
        color: '#999',
        textAlign: 'center',
        marginTop: 8,
    },
});

export default AdminProfileScreen;