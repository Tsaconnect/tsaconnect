import { useState, useEffect, useCallback } from 'react';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { type NetworkType, setActiveNetwork } from '../constants/chains';

const NETWORK_KEY = 'tsa-network';

let currentNetwork: NetworkType = 'mainnet';

export function getNetwork(): NetworkType {
  return currentNetwork;
}

export function useNetwork() {
  const [network, setNetwork] = useState<NetworkType>(currentNetwork);

  useEffect(() => {
    AsyncStorage.getItem(NETWORK_KEY).then(stored => {
      const net: NetworkType = stored === 'testnet' ? 'testnet' : 'mainnet';
      currentNetwork = net;
      setActiveNetwork(net);
      setNetwork(net);
    });
  }, []);

  const switchNetwork = useCallback(async (net: NetworkType) => {
    currentNetwork = net;
    setActiveNetwork(net);
    setNetwork(net);
    await AsyncStorage.setItem(NETWORK_KEY, net);
  }, []);

  return { network, switchNetwork };
}
