import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import BankDetails from '../../components/orders/bankdetails'
import { useLocalSearchParams } from 'expo-router'

const PaymentDetails = () => {
    const {accountNumber,accountName,bankName,id}=useLocalSearchParams()
  return (
    <View>
    <BankDetails accountName={accountName} bankName={bankName} accountNumber={accountNumber} id={id}/>
    </View>
  )
}

export default PaymentDetails

const styles = StyleSheet.create({})