// app/merchants/profile/index.tsx
//
// Merchant profile — avatar/name/role header, account-settings menu, referral
// copy, and logout. Restyled onto the TSA Signature design system (@/theme +
// @/components/ui primitives).
//
// VISUAL/STRUCTURAL restyle ONLY: token fetch (api.getStoredToken), profile
// fetch (api.getProfile), referral copy (expo-clipboard), email-verification
// (api.sendVerificationEmail), KYC + logout (api.clearAuth) handlers, all
// Alert/console messaging, the auth gate (redirect to /login), and every
// navigation route are preserved verbatim.
import api from '@/components/services/api';
import { useAuth } from '@/AuthContext/AuthContext';
import { router } from 'expo-router';
import React, { useEffect, useState } from 'react';
import { Alert, ScrollView, View } from 'react-native';
import * as Clipboard from 'expo-clipboard'; // Updated import
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme } from '@/theme';
import {
    AccentCard,
    Button,
    Card,
    Icon,
    IconName,
    PressableScale,
    Text,
} from '@/components/ui';

const MerchantProfileScreen = () => {
    const { colors, spacing, radius } = useTheme();
    const { logOut } = useAuth();
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
    // The route guard takes a signed-out user to /login.
    const handleLogout = async () => {
        try {
            await logOut();
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
        <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
            <ScrollView
                contentContainerStyle={{
                    padding: spacing.lg,
                    paddingBottom: spacing['4xl'],
                    flexGrow: 1,
                }}
            >
                {/* Profile header */}
                <AccentCard padding="xl" style={{ alignItems: 'center', marginBottom: spacing['2xl'] }}>
                    <View
                        style={{
                            width: 100,
                            height: 100,
                            borderRadius: radius.pill,
                            backgroundColor: colors.accent,
                            justifyContent: 'center',
                            alignItems: 'center',
                            marginBottom: spacing.lg,
                        }}
                    >
                        <Text variant="display" weight="700" style={{ color: colors.onAccent }}>
                            {getUserInitials(user?.name)}
                        </Text>
                    </View>
                    <Text variant="title2" weight="700" align="center">
                        {user?.name}
                    </Text>
                    {user?.role && (
                        <Text variant="caption" color="textMuted" align="center" style={{ marginTop: spacing.xs }}>
                            {user.role.toUpperCase()}
                        </Text>
                    )}
                </AccentCard>

                {/* Account settings */}
                <Text variant="overline" color="textMuted" style={{ marginBottom: spacing.sm }}>
                    ACCOUNT SETTINGS
                </Text>
                <View style={{ gap: spacing.sm }}>
                    <MenuRow icon="user" label="Edit Profile" />
                    <MenuRow icon="bank" label="Payout account" onPress={() => router.push("/merchants/payout-method")} />
                    <MenuRow icon="shield" label="Security & Privacy" />
                    <MenuRow icon="notifications" label="Notifications" />
                    {/* Optional: Add referral code section if needed */}
                    {user?.referralCode && (
                        <MenuRow
                            icon="copy"
                            label={`Copy Referral Code: ${user.referralCode}`}
                            onPress={copyToClipboard}
                        />
                    )}
                </View>

                {/* Logout */}
                <View style={{ flex: 1 }} />
                <Button
                    title="Log Out"
                    variant="danger"
                    leftIcon="close"
                    onPress={handleLogout}
                    style={{ marginTop: spacing['2xl'] }}
                />
            </ScrollView>
        </SafeAreaView>
    );
};

// Tappable settings row built on Card + PressableScale (mirrors the menu rows on
// the migrated wallet/p2p screens). `onPress` is optional so static rows match
// the legacy screen's non-navigating items exactly.
const MenuRow: React.FC<{ icon: IconName; label: string; onPress?: () => void }> = ({
    icon,
    label,
    onPress,
}) => {
    const { colors, spacing } = useTheme();
    return (
        <PressableScale onPress={onPress} disabled={!onPress}>
            <Card padding="md">
                <View style={{ flexDirection: 'row', alignItems: 'center', gap: spacing.md }}>
                    <Icon name={icon} size={20} color={colors.textSecondary} />
                    <Text variant="bodyMed" weight="500" style={{ flex: 1 }} numberOfLines={1}>
                        {label}
                    </Text>
                    {onPress && <Icon name="chevronRight" size={18} color={colors.textMuted} />}
                </View>
            </Card>
        </PressableScale>
    );
};

export default MerchantProfileScreen;
