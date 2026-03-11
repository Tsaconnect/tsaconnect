import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import AddProduct from '../../../../components/products/AddProduct'
import { useLocalSearchParams } from 'expo-router'
import AddService from '../../../../components/services/AddService'

const ServiceDetails = () => {
  const {index}=useLocalSearchParams();
  return (
    <View>
      {index==='0'&&<AddProduct/>}
      {index==='1'&&<AddService/>}

    </View>
  )
}

export default ServiceDetails

const styles = StyleSheet.create({})