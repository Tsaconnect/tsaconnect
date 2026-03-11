import { StyleSheet, Text, View } from 'react-native'
import React from 'react'
import ServiceDetailCard from '../../components/services/ServiceDetail'
import { router, useLocalSearchParams } from 'expo-router'
import { ADVERT_TYPE_SERVICE, CONTACT_FEE } from '../../constants/constantValues'
import { useAuth } from '../../AuthContext/AuthContext'

const ServiceDetail = () => {
  const { token,tTy, settTy } = useAuth();
    const {title,image,description}=useLocalSearchParams()
    function handlePay(){
router.push({pathname:'/orderproduct',params:{contactFee:CONTACT_FEE}})
settTy(ADVERT_TYPE_SERVICE)
    }
  return (
    <View>
      <ServiceDetailCard title={title} imageSrc={image} address={description} onPayPress={handlePay}/>
    </View>
  )
}

export default ServiceDetail

const styles = StyleSheet.create({})