import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import ConfirmationScreen from '../../components/orders/confirmation'
import PaymentReceivedScreen from '../../components/orders/paymentsuccess'

const confirmation = () => {
  return (
    <View>
     <ConfirmationScreen/>
     {/* <PaymentReceivedScreen/> */}
    </View>
  )
}

export default confirmation

const styles = StyleSheet.create({})