// app/admin/dashboard/index.tsx
import React from 'react';
import {
    View,
    Text,
    StyleSheet,
    ScrollView,
    TouchableOpacity,
    Dimensions,
} from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { Ionicons } from '@expo/vector-icons';
import { LinearGradient } from 'expo-linear-gradient';
import { useRouter } from 'expo-router';

const { width } = Dimensions.get('window');

export default function AdminDashboard() {
    const router = useRouter();

    const stats = [
        {
            title: 'Total Users',
            value: '1,234',
            icon: 'people-outline',
            color: '#8B4513',
            gradient: ['#8B4513', '#A0522D'],
            change: '+12%'
        },
        {
            title: 'Total Products',
            value: '567',
            icon: 'cube-outline',
            color: '#CD853F',
            gradient: ['#CD853F', '#DEB887'],
            change: '+5%'
        },
        {
            title: 'Pending Approvals',
            value: '23',
            icon: 'time-outline',
            color: '#D2691E',
            gradient: ['#D2691E', '#E9967A'],
            change: '-2%'
        },
        {
            title: 'Revenue',
            value: '₦45.6K',
            icon: 'cash-outline',
            color: '#8B4513',
            gradient: ['#8B4513', '#A0522D'],
            change: '+23%'
        },
    ];

    const quickActions = [
        {
            title: 'Merchant Approvals',
            icon: 'people',
            route: '/admin/merchant-approvals',
            count: 12
        },
        {
            title: 'Deposit Requests',
            icon: 'cash',
            route: '/admin/deposit-requests',
            count: 5
        },
        {
            title: 'Advert Requests',
            icon: 'megaphone',
            route: '/admin/advert-request',
            count: 3
        },
        {
            title: 'Add Category',
            icon: 'folder-open',
            route: '/admin/category/add',
            count: null
        },
    ];

    const recentActivities = [
        { action: 'New merchant registration', user: 'John Doe', time: '5 min ago' },
        { action: 'Deposit request', user: 'Jane Smith', time: '15 min ago', amount: '₦50,000' },
        { action: 'Product listed', user: 'ABC Store', time: '1 hour ago', product: 'iPhone 13' },
        { action: 'Advert submitted', user: 'XYZ Company', time: '2 hours ago' },
    ];

    return (
        <SafeAreaView style={styles.container}>
            <ScrollView showsVerticalScrollIndicator={false}>
                {/* Welcome Section */}
                <LinearGradient
                    colors={['#8B4513', '#A0522D', '#CD853F']}
                    start={{ x: 0, y: 0 }}
                    end={{ x: 1, y: 1 }}
                    style={styles.welcomeSection}
                >
                    <View style={styles.welcomeContent}>
                        <Text style={styles.welcomeText}>Welcome back,</Text>
                        <Text style={styles.adminName}>Admin User</Text>
                        <Text style={styles.dateText}>
                            {new Date().toLocaleDateString('en-US', {
                                weekday: 'long',
                                year: 'numeric',
                                month: 'long',
                                day: 'numeric'
                            })}
                        </Text>
                    </View>
                    <View style={styles.welcomeIcon}>
                        <Ionicons name="shield-checkmark" size={60} color="rgba(255,255,255,0.3)" />
                    </View>
                </LinearGradient>

                {/* Stats Grid */}
                <View style={styles.statsGrid}>
                    {stats.map((stat, index) => (
                        <LinearGradient
                            key={index}
                            //@ts-ignore
                            colors={stat.gradient}
                            start={{ x: 0, y: 0 }}
                            end={{ x: 1, y: 1 }}
                            style={styles.statCard}
                        >
                            <View style={styles.statIconContainer}>
                                <Ionicons name={stat.icon as any} size={24} color="#FFF" />
                            </View>
                            <Text style={styles.statValue}>{stat.value}</Text>
                            <Text style={styles.statTitle}>{stat.title}</Text>
                            <View style={styles.statChange}>
                                <Ionicons
                                    name={stat.change.startsWith('+') ? 'trending-up' : 'trending-down'}
                                    size={12}
                                    color="#FFF"
                                />
                                <Text style={styles.statChangeText}>{stat.change}</Text>
                            </View>
                        </LinearGradient>
                    ))}
                </View>

                {/* Quick Actions */}
                <View style={styles.section}>
                    <Text style={styles.sectionTitle}>Quick Actions</Text>
                    <View style={styles.quickActionsGrid}>
                        {quickActions.map((action, index) => (
                            <TouchableOpacity
                                key={index}
                                style={styles.quickActionCard}
                                onPress={() => router.push(action.route as any)}
                            >
                                <View style={styles.quickActionIcon}>
                                    <Ionicons name={action.icon as any} size={28} color="#8B4513" />
                                    {action.count && (
                                        <View style={styles.badge}>
                                            <Text style={styles.badgeText}>{action.count}</Text>
                                        </View>
                                    )}
                                </View>
                                <Text style={styles.quickActionTitle}>{action.title}</Text>
                            </TouchableOpacity>
                        ))}
                    </View>
                </View>

                {/* Recent Activity */}
                <View style={styles.section}>
                    <Text style={styles.sectionTitle}>Recent Activity</Text>
                    {recentActivities.map((activity, index) => (
                        <View key={index} style={styles.activityCard}>
                            <View style={styles.activityIcon}>
                                <Ionicons
                                    name={activity.action.includes('merchant') ? 'person-add' :
                                        activity.action.includes('Deposit') ? 'cash' :
                                            activity.action.includes('Product') ? 'cube' : 'megaphone'}
                                    size={20}
                                    color="#8B4513"
                                />
                            </View>
                            <View style={styles.activityContent}>
                                <Text style={styles.activityAction}>{activity.action}</Text>
                                <Text style={styles.activityUser}>{activity.user}</Text>
                                {activity.amount && (
                                    <Text style={styles.activityAmount}>{activity.amount}</Text>
                                )}
                            </View>
                            <Text style={styles.activityTime}>{activity.time}</Text>
                        </View>
                    ))}
                </View>

                {/* System Status */}
                <View style={styles.section}>
                    <Text style={styles.sectionTitle}>System Status</Text>
                    <View style={styles.statusCard}>
                        <View style={styles.statusItem}>
                            <View style={styles.statusLeft}>
                                <Ionicons name="server-outline" size={20} color="#8B4513" />
                                <Text style={styles.statusLabel}>Server Status</Text>
                            </View>
                            <View style={styles.statusRight}>
                                <View style={styles.statusDot} />
                                <Text style={styles.statusValue}>Healthy</Text>
                            </View>
                        </View>
                        <View style={styles.statusItem}>
                            <View style={styles.statusLeft}>
                                <Ionicons name="cloud-outline" size={20} color="#8B4513" />
                                <Text style={styles.statusLabel}>Database</Text>
                            </View>
                            <View style={styles.statusRight}>
                                <View style={styles.statusDot} />
                                <Text style={styles.statusValue}>Connected</Text>
                            </View>
                        </View>
                        <View style={styles.statusItem}>
                            <View style={styles.statusLeft}>
                                <Ionicons name="shield-outline" size={20} color="#8B4513" />
                                <Text style={styles.statusLabel}>Last Backup</Text>
                            </View>
                            <Text style={styles.statusValue}>2 hours ago</Text>
                        </View>
                    </View>
                </View>
            </ScrollView>
        </SafeAreaView>
    );
}

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: '#F5F5F5',
    },
    welcomeSection: {
        flexDirection: 'row',
        padding: 20,
        paddingTop: 30,
        paddingBottom: 30,
        borderBottomLeftRadius: 30,
        borderBottomRightRadius: 30,
        marginBottom: 20,
    },
    welcomeContent: {
        flex: 1,
    },
    welcomeText: {
        fontSize: 14,
        color: 'rgba(255,255,255,0.9)',
        marginBottom: 5,
    },
    adminName: {
        fontSize: 24,
        fontWeight: '700',
        color: '#FFFFFF',
        marginBottom: 5,
    },
    dateText: {
        fontSize: 12,
        color: 'rgba(255,255,255,0.8)',
    },
    welcomeIcon: {
        justifyContent: 'center',
        alignItems: 'center',
    },
    statsGrid: {
        flexDirection: 'row',
        flexWrap: 'wrap',
        paddingHorizontal: 10,
        marginBottom: 20,
    },
    statCard: {
        width: (width - 40) / 2,
        marginHorizontal: 5,
        marginBottom: 10,
        padding: 15,
        borderRadius: 15,
        shadowColor: '#8B4513',
        shadowOffset: { width: 0, height: 4 },
        shadowOpacity: 0.2,
        shadowRadius: 8,
        elevation: 5,
    },
    statIconContainer: {
        width: 40,
        height: 40,
        borderRadius: 20,
        backgroundColor: 'rgba(255,255,255,0.2)',
        justifyContent: 'center',
        alignItems: 'center',
        marginBottom: 10,
    },
    statValue: {
        fontSize: 20,
        fontWeight: '700',
        color: '#FFFFFF',
        marginBottom: 2,
    },
    statTitle: {
        fontSize: 12,
        color: 'rgba(255,255,255,0.9)',
        marginBottom: 5,
    },
    statChange: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    statChangeText: {
        fontSize: 10,
        color: '#FFFFFF',
        marginLeft: 2,
    },
    section: {
        paddingHorizontal: 20,
        marginBottom: 25,
    },
    sectionTitle: {
        fontSize: 18,
        fontWeight: '700',
        color: '#333',
        marginBottom: 15,
    },
    quickActionsGrid: {
        flexDirection: 'row',
        flexWrap: 'wrap',
        justifyContent: 'space-between',
    },
    quickActionCard: {
        width: (width - 50) / 2,
        backgroundColor: '#FFFFFF',
        padding: 20,
        borderRadius: 15,
        alignItems: 'center',
        marginBottom: 10,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    quickActionIcon: {
        width: 50,
        height: 50,
        borderRadius: 25,
        backgroundColor: 'rgba(139, 69, 19, 0.1)',
        justifyContent: 'center',
        alignItems: 'center',
        marginBottom: 10,
        position: 'relative',
    },
    badge: {
        position: 'absolute',
        top: -5,
        right: -5,
        backgroundColor: '#FF6B6B',
        borderRadius: 10,
        minWidth: 20,
        height: 20,
        justifyContent: 'center',
        alignItems: 'center',
    },
    badgeText: {
        color: '#FFFFFF',
        fontSize: 10,
        fontWeight: '600',
    },
    quickActionTitle: {
        fontSize: 13,
        fontWeight: '500',
        color: '#333',
        textAlign: 'center',
    },
    activityCard: {
        flexDirection: 'row',
        backgroundColor: '#FFFFFF',
        padding: 15,
        borderRadius: 12,
        marginBottom: 10,
        alignItems: 'center',
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    activityIcon: {
        width: 40,
        height: 40,
        borderRadius: 20,
        backgroundColor: 'rgba(139, 69, 19, 0.1)',
        justifyContent: 'center',
        alignItems: 'center',
        marginRight: 12,
    },
    activityContent: {
        flex: 1,
    },
    activityAction: {
        fontSize: 14,
        fontWeight: '600',
        color: '#333',
        marginBottom: 2,
    },
    activityUser: {
        fontSize: 12,
        color: '#666',
    },
    activityAmount: {
        fontSize: 12,
        color: '#8B4513',
        fontWeight: '600',
        marginTop: 2,
    },
    activityTime: {
        fontSize: 11,
        color: '#999',
    },
    statusCard: {
        backgroundColor: '#FFFFFF',
        padding: 15,
        borderRadius: 12,
        shadowColor: '#000',
        shadowOffset: { width: 0, height: 2 },
        shadowOpacity: 0.05,
        shadowRadius: 4,
        elevation: 2,
    },
    statusItem: {
        flexDirection: 'row',
        justifyContent: 'space-between',
        alignItems: 'center',
        paddingVertical: 10,
        borderBottomWidth: 1,
        borderBottomColor: 'rgba(0,0,0,0.05)',
    },
    statusLeft: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    statusLabel: {
        fontSize: 14,
        color: '#666',
        marginLeft: 10,
    },
    statusRight: {
        flexDirection: 'row',
        alignItems: 'center',
    },
    statusDot: {
        width: 8,
        height: 8,
        borderRadius: 4,
        backgroundColor: '#4CAF50',
        marginRight: 6,
    },
    statusValue: {
        fontSize: 14,
        color: '#333',
        fontWeight: '500',
    },
});