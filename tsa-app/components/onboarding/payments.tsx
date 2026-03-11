import { Image, StyleSheet, Text, View } from 'react-native'
import React from 'react'
import { COLORS, SIZES } from '../../constants/theme'
import { SkipNextButton } from '../buttons/NextSkip'
import { router } from 'expo-router'

const Payments = () => {
    function handleNext(){
        router.push('/order')
      }
      function handleSkip(){
     router.push('/login')
      }
  return (
    <View style={styles.container}>
    <View style={{top:SIZES.height*(0.07618),position:'absolute',alignContent:'center',alignItems:'center'}}>
      <Text style={{color:COLORS.primary,fontWeight:600,fontSize:24}}>Make payments across </Text>
     <Text style={{color:COLORS.primary,fontWeight:600,fontSize:24}}>the borders</Text>  
     <Text style={{color:COLORS.primary,fontSize:15,fontWeight:400}}>Buy & sell bitcoin, mcgp, & other crypto and</Text>
     <Text style={{color:COLORS.primary,fontSize:15,fontWeight:400}}>digital assets. Easy assets swap on dex and more</Text>
      </View>
    <View style={{position:'absolute', alignContent:'center',width:SIZES.width,height:SIZES.height*(0.504),alignItems:'center',top:SIZES.height*(0.236)}}>
    <Image source={require("../../assets/images/onboarding2.png")} 
     style={{ width: "99%" }}
     resizeMode='contain'
    />
    </View>
    <View style={styles.back}>
    <SkipNextButton handleNext={handleNext} handleSkip={handleSkip} index={2}/>
    </View>
   
  </View>
  )
}

export default Payments

const styles = StyleSheet.create({
    container: {
        flex: 1,
        backgroundColor: '#fff',
        alignItems: 'center',
        justifyContent: 'center',
        marginBottom: 0,
      //  paddingTop: StatusBar.currentHeight || 0,
      },
      back:{
        position:'absolute',
        top:SIZES.height*(0.8079)
      }
})