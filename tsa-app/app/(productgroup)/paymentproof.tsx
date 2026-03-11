import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import PaymentProof from '../../components/orders/paymentproof'
import { useLocalSearchParams } from 'expo-router'

const paymentproof = () => {
    const {id}=useLocalSearchParams()
  return (
    <View>
     <PaymentProof id={id}/>
    </View>
  )
}

export default paymentproof

const styles = StyleSheet.create({})