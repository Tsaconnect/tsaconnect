import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import Delivery from '../../components/orders/delivery'
import { useLocalSearchParams } from 'expo-router';

const orderproduct = () => {
  const {totalAmount,contactFee} = useLocalSearchParams();
  return (
    <Delivery data={totalAmount||contactFee}/>
  )
}

export default orderproduct

const styles = StyleSheet.create({})