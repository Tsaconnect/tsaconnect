// app/(productgroup)/orderproduct.tsx
//
// Order/checkout entry for a product — reads the order total (totalAmount, or the
// service contact fee fallback) from the route params and hands it to the Delivery
// step, which captures the shipping address and forwards to /paymenttype.
//
// Migrated onto the TSA Signature design system: the screen chrome now lives in the
// themed Delivery component (rebuilt on @/theme + @/components/ui). This wrapper is
// pure delegation — the param read and the data={totalAmount||contactFee} handoff
// are preserved verbatim.
import React from 'react';
import { useLocalSearchParams } from 'expo-router';
import Delivery from '../../components/orders/delivery';

const OrderProductRoute = () => {
  const { totalAmount, contactFee } = useLocalSearchParams();
  return (
    //@ts-ignore preserve original param-to-prop handoff
    <Delivery data={totalAmount || contactFee} />
  );
};

export default OrderProductRoute;
