import { StyleSheet, Text, TouchableOpacity, View } from 'react-native'
import React from 'react'
import { OtpInput } from "react-native-otp-entry";
import { COLORS, SIZES } from '../../constants/theme';
const Verify = ({payLoad}) => {
    const [verificationCode, setVerificationCode] = React.useState();
    
  return (
    <View style={styles.wrapper}>
       <View>
       <Text style={styles.loginText}>Verify your Account
        </Text >
        <Text style={styles.subText}>enter verification code send to {"Email here"}</Text>
       </View>
        
     <OtpInput 
numberOfDigits={6} 
onTextChange={(text)=>setVerificationCode(text)}
onFilled={(text) => console.log(`OTP is ${text}`)}
  theme={{
    containerStyle: styles.container,
    inputsContainerStyle: styles.inputsContainer,
    pinCodeContainerStyle: styles.pinCodeContainer,
    pinCodeTextStyle: styles.pinCodeText,
    focusStickStyle: styles.focusStick,
    focusedPinCodeContainerStyle: styles.activePinCodeContainer,
    
  }}
  
/>
<TouchableOpacity style={styles.button}>
        <Text style={styles.buttonText}>Continue</Text>
      </TouchableOpacity>
    </View>
  )
}

export default Verify

const styles = StyleSheet.create({
    container: {
        /*  flex: 1, */
         backgroundColor: '#fff',
       },
       wrapper: {
         justifyContent: 'center',
         top:SIZES.height*(0.2121)
       },
       button: {
         marginTop: 20,
         height: 50,
         width: 300,
         justifyContent: 'center',
         alignItems: 'center',
         backgroundColor: COLORS.primary,
         shadowColor: 'rgba(0,0,0,0.4)',
         shadowOffset: {
           width: 1,
           height: 5,
         },
         shadowOpacity: 0.34,
         shadowRadius: 6.27,
         elevation: 10,
       },
       buttonText: {
         color: 'white',
         fontSize: 14,
       },
       redColor: {
         backgroundColor: '#F57777',
       },
       message: {
         borderWidth: 1,
         borderRadius: 5,
         padding: 20,
         marginBottom: 20,
         justifyContent: 'center',
         alignItems: 'flex-start',
       },
       buttonContainer: {
         backgroundColor: COLORS.primary,
         paddingVertical: 10,
         paddingHorizontal: 20,
         borderRadius: 15,
         flexDirection: 'row',
         alignItems: 'center',
         justifyContent:'center',
         borderWidth: 2,
         width: '80%',
         margin: 10,
         height:SIZES.height/14
       },
       switchContainer: {
         flexDirection: 'row',
         alignItems: 'center',
         justifyContent: 'space-between',
         width: '80%',
         marginBottom: 20,
       },
       container: {
         marginTop: 20, // Adjust as needed
         flexDirection: 'row',
         justifyContent: 'space-between',
       },
       inputsContainer: {
         alignItems: 'center',
         justifyContent: 'center',
       },
       pinCodeContainer: {
         borderWidth: 1,
         borderColor: 'black',
         borderRadius: 5,
         padding: 10,
         marginHorizontal: 5,
       },
       pinCodeText: {
         fontSize: 18,
         textAlign: 'center',
       },
       focusStick: {
         backgroundColor: 'green',
         width: 2,
         borderRadius: 5,
       },
       activePinCodeContainer: {
         borderColor: 'green',
       },
       loginText: {
        fontSize: 24,
        fontWeight: "bold",
        left: "5%",
        color: COLORS.primary
      },
      subText:{
        left: "5%",
      },
      button:{
        backgroundColor: COLORS.primary,
        borderRadius: 15,
        flexDirection: 'row',
        alignItems: 'center',
        justifyContent: 'center',
        width: '90%',
        marginVertical: SIZES.height*(0.026824),
        height:SIZES.height*(0.068),
        borderBlockColor:COLORS.primary,
        marginHorizontal:'5%'
      },
})