// app/admin/_layout.tsx
import { Drawer } from 'expo-router/drawer';
import { GestureHandlerRootView } from 'react-native-gesture-handler';
import { Ionicons } from '@expo/vector-icons';
import { View, Text, TouchableOpacity, StyleSheet, Image, SafeAreaView, Platform } from 'react-native';
import { useRouter } from 'expo-router';
import { DrawerContentScrollView, DrawerItemList } from '@react-navigation/drawer';
import { LinearGradient } from 'expo-linear-gradient';

// Custom drawer content with toggle button
function CustomDrawerContent(props: any) {
    const router = useRouter();

    const handleToggleToUser = () => {
        // Navigate to home screen (user interface)
        router.push('/home');
    };

    return (
        <View style={styles.drawerContainer}>
            {/* Header with gradient */}
            <LinearGradient
                colors={['#8B4513', '#A0522D', '#CD853F']}
                start={{ x: 0, y: 0 }}
                end={{ x: 1, y: 1 }}
                style={styles.drawerHeader}
            >
                <View style={styles.logoContainer}>
                    <View style={styles.logoCircle}>
                        <Ionicons name="storefront" size={32} color="#8B4513" />
                    </View>
                    <View style={styles.logoTextContainer}>
                        <Text style={styles.logoTitle}>Admin Panel</Text>
                        <Text style={styles.logoSubtitle}>TSA Marketplace</Text>
                    </View>
                </View>

                {/* Admin Profile Summary */}
                <View style={styles.profileSummary}>
                    <View style={styles.profileAvatar}>
                        <Text style={styles.profileAvatarText}>A</Text>
                    </View>
                    <View style={styles.profileInfo}>
                        <Text style={styles.profileName}>Admin User</Text>
                        <Text style={styles.profileRole}>Super Administrator</Text>
                    </View>
                </View>
            </LinearGradient>

            {/* Drawer Items */}
            <DrawerContentScrollView
                {...props}
                contentContainerStyle={styles.drawerContent}
                showsVerticalScrollIndicator={false}
            >
                <DrawerItemList {...props} />
            </DrawerContentScrollView>

            {/* Bottom Section with Toggle Button */}
            <View style={styles.bottomSection}>
                {/* Toggle to User Interface Button */}
                <TouchableOpacity
                    style={styles.toggleButton}
                    onPress={handleToggleToUser}
                    activeOpacity={0.8}
                >
                    <LinearGradient
                        colors={['#8B4513', '#A0522D']}
                        start={{ x: 0, y: 0 }}
                        end={{ x: 1, y: 0 }}
                        style={styles.toggleGradient}
                    >
                        <View style={styles.toggleContent}>
                            <View style={styles.toggleIconContainer}>
                                <Ionicons name="swap-horizontal" size={20} color="#FFF" />
                            </View>
                            <View style={styles.toggleTextContainer}>
                                <Text style={styles.toggleLabel}>Switch to</Text>
                                <Text style={styles.toggleTitle}>User Interface</Text>
                            </View>
                            <Ionicons name="arrow-forward" size={20} color="#FFF" />
                        </View>
                    </LinearGradient>
                </TouchableOpacity>

                {/* Version Info */}
                <Text style={styles.versionText}>Version 2.0.0 • LOCOH Admin</Text>
            </View>
        </View>
    );
}

export default function AdminLayout() {
    const router = useRouter();

    return (
        <GestureHandlerRootView style={{ flex: 1 }}>
            <Drawer
                drawerContent={(props) => <CustomDrawerContent {...props} />}
                screenOptions={{
                    headerShown: true,
                    drawerActiveTintColor: '#8B4513',
                    drawerInactiveTintColor: '#666',
                    drawerActiveBackgroundColor: 'rgba(139, 69, 19, 0.1)',
                    drawerStyle: {
                        backgroundColor: '#FFFFFF',
                        width: 300,
                        borderTopRightRadius: 20,
                        borderBottomRightRadius: 20,
                        shadowColor: '#8B4513',
                        shadowOffset: { width: 5, height: 0 },
                        shadowOpacity: 0.1,
                        shadowRadius: 10,
                        elevation: 5,
                    },
                    drawerLabelStyle: {
                        fontSize: 14,
                        fontWeight: '500',
                        marginLeft: -10,
                    },
                    drawerItemStyle: {
                        borderRadius: 8,
                        marginHorizontal: 10,
                        marginVertical: 4,
                    },
                    headerStyle: {
                        backgroundColor: '#8B4513',
                        elevation: 0,
                        shadowOpacity: 0,
                        borderBottomWidth: 0,
                    },
                    headerTintColor: '#FFFFFF',
                    headerTitleStyle: {
                        fontWeight: '600',
                        fontSize: 18,
                    },
                    headerRight: () => (
                        <TouchableOpacity
                            style={styles.headerRightButton}
                            onPress={() => router.push('/profile')}
                        >
                            <View style={styles.headerAvatar}>
                                <Text style={styles.headerAvatarText}>A</Text>
                            </View>
                        </TouchableOpacity>
                    ),
                }}
            >
                <Drawer.Screen
                    name="dashboard/index"
                    options={{
                        drawerLabel: 'Dashboard',
                        title: 'Admin Dashboard',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="grid-outline" size={size} color={color} />
                        ),
                    }}
                />

                <Drawer.Screen
                    name="category/display/index"
                    options={{
                        drawerLabel: 'All Categories',
                        title: 'Manage Categories',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="folder-outline" size={size} color={color} />
                        ),
                    }}
                />

                <Drawer.Screen
                    name="category/add/index"
                    options={{
                        drawerLabel: 'Add Category',
                        title: 'Add New Category',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="add-circle-outline" size={size} color={color} />
                        ),
                    }}
                />

                <Drawer.Screen
                    name="deposit-requests/index"
                    options={{
                        drawerLabel: 'Deposit Requests',
                        title: 'Deposit Requests',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="cash-outline" size={size} color={color} />
                        ),
                    }}
                />

                <Drawer.Screen
                    name="advert-request/index"
                    options={{
                        drawerLabel: 'Advert Requests',
                        title: 'Advert Requests',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="megaphone-outline" size={size} color={color} />
                        ),
                    }}
                />

                <Drawer.Screen
                    name="profile/index"
                    options={{
                        drawerLabel: 'Profile',
                        title: 'My Profile',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="person-outline" size={size} color={color} />
                        ),
                    }}
                />

                {/* Separator Line */}
                <Drawer.Screen
                    name="separator"
                    options={{
                        drawerLabel: () => <View style={styles.separator} />,
                        drawerItemStyle: { height: 1, backgroundColor: 'rgba(139, 69, 19, 0.1)', marginVertical: 10 },
                    }}
                />

                <Drawer.Screen
                    name="settings/index"
                    options={{
                        drawerLabel: 'Settings',
                        title: 'Settings',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="settings-outline" size={size} color={color} />
                        ),
                    }}
                />

                <Drawer.Screen
                    name="help/index"
                    options={{
                        drawerLabel: 'Help & Support',
                        title: 'Help & Support',
                        drawerIcon: ({ color, size }) => (
                            <Ionicons name="help-circle-outline" size={size} color={color} />
                        ),
                    }}
                />
            </Drawer>
        </GestureHandlerRootView>
    );
}

const styles = StyleSheet.create({
    drawerContainer: {
        flex: 1,
        backgroundColor: '#FFFFFF',
    },
    drawerHeader: {
        paddingTop: Platform.OS === 'ios' ? 50 : 30,
        paddingBottom: 20,
        paddingHorizontal: 20,
        borderBottomLeftRadius: 25,
        borderBottomRightRadius: 25,
    },
    logoContainer: {
        flexDirection: 'row',
        alignItems: 'center',
        marginBottom: 20,
    },
    logoCircle: {
        width: 50,
        height: 50,
        borderRadius: 25,
        backgroundColor: '#FFFFFF',
        justifyContent: 'center',
        alignItems: 'center',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.1,
        shadowRadius: 4,
        elevation: 3,
    },
    logoTextContainer: {
        marginLeft: 12,
    },
    logoTitle: {
        fontSize: 18,
        fontWeight: '700',
        color: '#FFFFFF',
    },
    logoSubtitle: {
        fontSize: 12,
        color: 'rgba(255,255,255,0.9)',
        marginTop: 2,
    },
    profileSummary: {
        flexDirection: 'row',
        alignItems: 'center',
        backgroundColor: 'rgba(255,255,255,0.2)',
        padding: 12,
        borderRadius: 12,
        marginTop: 10,
    },
    profileAvatar: {
        width: 45,
        height: 45,
        borderRadius: 23,
        backgroundColor: '#CD853F',
        justifyContent: 'center',
        alignItems: 'center',
        borderWidth: 2,
        borderColor: '#FFFFFF',
    },
    profileAvatarText: {
        fontSize: 18,
        fontWeight: '700',
        color: '#FFFFFF',
    },
    profileInfo: {
        marginLeft: 12,
    },
    profileName: {
        fontSize: 14,
        fontWeight: '600',
        color: '#FFFFFF',
    },
    profileRole: {
        fontSize: 11,
        color: 'rgba(255,255,255,0.8)',
        marginTop: 2,
    },
    drawerContent: {
        paddingTop: 10,
    },
    bottomSection: {
        padding: 20,
        borderTopWidth: 1,
        borderTopColor: 'rgba(139, 69, 19, 0.1)',
        backgroundColor: '#FFFFFF',
    },
    toggleButton: {
        borderRadius: 12,
        overflow: 'hidden',
        marginBottom: 12,
        shadowColor: '#8B4513',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.2,
        shadowRadius: 8,
        elevation: 5,
    },
    toggleGradient: {
        paddingVertical: 14,
        paddingHorizontal: 16,
    },
    toggleContent: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    toggleIconContainer: {
        width: 36,
        height: 36,
        borderRadius: 18,
        backgroundColor: 'rgba(255,255,255,0.2)',
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 12,
    },
    toggleTextContainer: {
        flex: 1,
    },
    toggleLabel: {
        fontSize: 11,
        color: 'rgba(255,255,255,0.8)',
        marginBottom: 2,
    },
    toggleTitle: {
        fontSize: 15,
        fontWeight: '700',
        color: '#FFFFFF',
    },
    versionText: {
        fontSize: 11,
        color: '#999',
        textAlign: 'center',
        marginTop: 8,
    },
    separator: {
        height: 1,
        backgroundColor: 'transparent',
    },
    headerRightButton: {
        marginRight: 16,
    },
    headerAvatar: {
        width: 35,
        height: 35,
        borderRadius: 18,
        backgroundColor: '#CD853F',
        justifyContent: 'center',
        alignItems: 'center',
        borderWidth: 2,
        borderColor: '#FFFFFF',
    },
    headerAvatarText: {
        fontSize: 14,
        fontWeight: '600',
        color: '#FFFFFF',
    },
});