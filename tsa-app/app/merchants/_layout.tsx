import { Stack } from 'expo-router';
import { COLORS } from '../../constants';

export default function MerchantLayout() {
    return (
        <Stack
            screenOptions={{
                headerShown: false,
            }}
        >
            <Stack.Screen
                name="dashboard/index"
                options={{
                    title: 'Merchant Dashboard',
                }}
            />
            <Stack.Screen
                name="inventory/index"
                options={{
                    title: 'Manage Inventory',
                }}
            />
            <Stack.Screen
                name="inventory/add/index"
                options={{
                    title: 'Add Product',
                }}
            />
            <Stack.Screen
                name="inventory/edit/[productId]"
                options={{
                    title: 'Edit Product',
                }}
            />
            <Stack.Screen
                name="sell-usdt/index"
                options={{
                    title: 'P2P: Sell USDT',
                }}
            />
            <Stack.Screen
                name="buy-usdt/index"
                options={{
                    title: 'P2P: Buy USDT/USDC',
                }}
            />
            <Stack.Screen
                name="digital/index"
                options={{
                    title: 'Digital Assets',
                }}
            />
            <Stack.Screen
                name="profile/index"
                options={{
                    title: 'Profile',
                }}
            />
            <Stack.Screen
                name="merchant-request"
                options={{
                    title: 'Merchant Application',
                }}
            />
        </Stack>
    );
}
