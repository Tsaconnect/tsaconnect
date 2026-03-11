import { StyleSheet, Text, View,Image } from 'react-native'
import React from 'react'
import { COLORS, SIZES } from '../../constants/theme'
import { SkipNextButton } from '../buttons/NextSkip'
import { router } from 'expo-router'

const Order = () => {
    function handleNext(){
      router.push('/login')
      }
      function handleSkip(){
        router.push('/login')
      }
  return (
    <View style={styles.container}>
   
    <View style={{top:SIZES.height*(0.143),position:'absolute',alignContent:'center',alignItems:'center'}}>
      <Text style={{color:COLORS.primary,fontWeight:'bold',fontSize:25}}>Order for services </Text>
     <Text style={{color:COLORS.primary,fontWeight:'bold',fontSize:25}}>anytime, anywhere</Text>  
      </View>
    <View style={{ width:SIZES.width*(0.81),position:'absolute',alignContent:'center',height:SIZES.height*(0.5644) }}>
    <Image source={require("../../assets/images/onboarding3.png")} 
     style={{ width: '100%',height:'100%',marginTop:'4%'}}
     resizeMode='contain'
    />
    </View>
    <View style={styles.back}>
    <SkipNextButton handleNext={handleNext} handleSkip={handleSkip} index={3}/>
    </View>
  </View>
  )
}

export default Order

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
        top:SIZES.height*(0.84656)
      }
})