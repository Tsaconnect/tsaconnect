// app/merchants/_layout.tsx
//
// Stack layout for the Merchant section (dashboard, inventory, orders, P2P
// buy/sell, digital assets, profile, merchant application).
//
// Headers used to be hidden here on the theory that "each merchant screen
// renders its own chrome". Five of the thirteen rendered none at all
// (dashboard, inventory, buy-usdt, digital, profile) and the other eight
// hand-rolled a chevron row, each slightly different. Since `merchants/` is a
// ROOT-level group it has neither the tab bar nor the drawer to fall back on,
// so those five were complete dead ends on iOS.
//
// The titles below already existed; showing the header is what makes them do
// any work. The screens' own big page headings stay — they read as content, and
// the header supplies the back chevron they were all missing.
import React from 'react';
import { Stack } from 'expo-router';
import { useDefaultScreenOptions } from '@/constants/navigation';
import { HOME_TAB } from '@/constants/routes';

export default function MerchantLayout() {
    const screenOptions = useDefaultScreenOptions({ backFallback: HOME_TAB });

    return (
        <Stack screenOptions={screenOptions}>
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
                    title: 'Sell USDT',
                }}
            />
            <Stack.Screen
                name="buy-usdt/index"
                options={{
                    title: 'Buy Crypto',
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
            <Stack.Screen
                name="pay-settings"
                options={{
                    title: 'Payment Settings',
                }}
            />
            <Stack.Screen
                name="activate-pm"
                options={{
                    title: 'Activate Payment Merchant',
                }}
            />
        </Stack>
    );
}
