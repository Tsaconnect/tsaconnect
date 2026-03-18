import { Drawer } from 'expo-router/drawer';
import { Ionicons } from '@expo/vector-icons';

export default function MerchantLayout() {
    return (
        <Drawer
            screenOptions={{
                headerShown: true,
                drawerActiveTintColor: '#9b795fff',
                drawerInactiveTintColor: '#64748b',
            }}
        >
            <Drawer.Screen
                name="dashboard/index"
                options={{
                    drawerLabel: 'Merchant Dashboard',
                    title: 'Overview',
                    drawerIcon: ({ color, size }) => <Ionicons name="grid-outline" size={size} color={color} />,
                }}
            />
            <Drawer.Screen
                name="inventory/index"
                options={{
                    drawerLabel: 'Products & Services',
                    title: 'Manage Inventory',
                    drawerIcon: ({ color, size }) => <Ionicons name="cart-outline" size={size} color={color} />,
                }}
            />
            <Drawer.Screen
                name="sell-usdt/index"
                options={{
                    drawerLabel: 'Sell USDT for Fiat',
                    title: 'P2P: Sell USDT',
                    drawerIcon: ({ color, size }) => <Ionicons name="trending-up-outline" size={size} color={color} />,
                }}
            />
            <Drawer.Screen
                name="buy-usdt/index"
                options={{
                    drawerLabel: 'Buy Crypto with Cash',
                    title: 'P2P: Buy USDT/USDC',
                    drawerIcon: ({ color, size }) => <Ionicons name="cash-outline" size={size} color={color} />,
                }}
            />
            <Drawer.Screen
                name="digital/index"
                options={{
                    drawerLabel: 'Digital Assets',
                    title: 'Digital Assets',
                    drawerIcon: ({ color, size }) => <Ionicons name="wallet-outline" size={size} color={color} />,
                }}
            />
            <Drawer.Screen
                name="inventory/add/index"
                options={{
                    drawerLabel: 'Add Product',
                    title: 'Add Product',
                    drawerIcon: ({ color, size }) => <Ionicons name="add-circle-outline" size={size} color={color} />,
                }}
            />
            <Drawer.Screen
                name="profile/index"
                options={{
                    drawerLabel: 'Profile',
                    title: 'Profile',
                    drawerIcon: ({ color, size }) => <Ionicons name="person-outline" size={size} color={color} />,
                }}
            />
            <Drawer.Screen
                name="merchant-request"
                options={{
                    drawerLabel: 'Merchant Application',
                    title: 'Merchant Application',
                    drawerItemStyle: { display: 'none' },
                    drawerIcon: ({ color, size }) => <Ionicons name="document-text-outline" size={size} color={color} />,
                }}
            />
        </Drawer>
    );
}
