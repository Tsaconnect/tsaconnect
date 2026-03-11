import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import CryptoDetails from '../../components/orders/cryptodetails'
import { useLocalSearchParams } from 'expo-router'

const cryptodetails = () => {
    const {walletAddress,symbol,network,id}=useLocalSearchParams()
  return (
    <View>
    <CryptoDetails
     walletAddress={walletAddress}
     symbol={symbol}
     network={network}
     id={id}
    />
    </View>
  )
}

export default cryptodetails

const styles = StyleSheet.create({})