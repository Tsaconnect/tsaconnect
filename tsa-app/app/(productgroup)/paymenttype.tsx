import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import PaymentSelection from '../../components/orders/PaymentType'
import { useLocalSearchParams } from 'expo-router'

const PaymentTypeScren = () => {
    const {address,state,city,country,phoneNumber,totalAmount,fullName}=useLocalSearchParams();

  return (
    <View>
    <PaymentSelection address={address} 
    state={state} 
    city={city} 
    country={country}
    phoneNumber={phoneNumber}
    totalAmount={totalAmount}
    fullName={fullName}
    />
    </View>
  )
}

export default PaymentTypeScren

const styles = StyleSheet.create({})