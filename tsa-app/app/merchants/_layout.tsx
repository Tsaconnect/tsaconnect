// app/merchants/_layout.tsx
//
// Stack layout for the Merchant section (dashboard, inventory, orders, P2P
// buy/sell, digital assets, profile, merchant application). Migrated onto the
// TSA Signature design system: header chrome (background / tint / title color /
// font) is now sourced from useTheme() tokens instead of implicit/legacy
// styling, so it tracks light & dark automatically. Screen registrations,
// names, order, and titles are preserved verbatim. Headers stay hidden at this
// level (`headerShown: false`) exactly as before — each merchant screen renders
// its own chrome — but the themed options are in place for any screen that opts
// a header back on.
import React from 'react';
import { Stack } from 'expo-router';
import type { NativeStackNavigationOptions } from '@react-navigation/native-stack';
import { useTheme, fontFamily } from '@/theme';

export default function MerchantLayout() {
    const { colors, type } = useTheme();

    const screenOptions: NativeStackNavigationOptions = {
        headerShown: false,
        headerTitleAlign: 'center',
        headerTintColor: colors.accent,
        headerTitleStyle: {
            fontSize: type.title3.fontSize,
            fontFamily: fontFamily['600'],
            color: colors.textPrimary,
        },
        headerStyle: {
            backgroundColor: colors.surface,
        },
        headerShadowVisible: false,
        contentStyle: {
            backgroundColor: colors.background,
        },
    };

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
            <Stack.Screen
                name="activate-pm"
                options={{
                    title: 'Activate Payment Merchant',
                    headerShown: true,
                }}
            />
        </Stack>
    );
}
