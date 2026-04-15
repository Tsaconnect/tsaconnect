import { Stack } from 'expo-router';

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
                name="inventory/[productId]"
                options={{
                    title: 'Product Details',
                }}
            />
            <Stack.Screen
                name="inventory/edit/[productId]"
                options={{
                    title: 'Edit Product',
                }}
            />
            <Stack.Screen
                name="orders/index"
                options={{
                    title: 'Orders',
                }}
            />
            <Stack.Screen
                name="orders/[orderId]"
                options={{
                    title: 'Order Details',
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
