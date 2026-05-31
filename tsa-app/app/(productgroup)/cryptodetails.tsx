// app/(productgroup)/cryptodetails.tsx
//
// Crypto payment details route — reads the deposit params (walletAddress,
// symbol, network, id) from the navigation params and renders the CryptoDetails
// screen component. Migrated onto the TSA Signature design system: the legacy
// bare <View> wrapper (and its empty StyleSheet) is dropped — CryptoDetails now
// owns its themed SafeAreaView chrome. The params read and pass-through are
// preserved verbatim.
import React from "react";
import { useLocalSearchParams } from "expo-router";
import CryptoDetails from "../../components/orders/cryptodetails";

const CryptoDetailsRoute = () => {
  const { walletAddress, symbol, network, id } = useLocalSearchParams<{
    walletAddress: string;
    symbol: string;
    network: string;
    id: string;
  }>();
  return (
    <CryptoDetails
      walletAddress={walletAddress}
      symbol={symbol}
      network={network}
      id={id}
    />
  );
};

export default CryptoDetailsRoute;
