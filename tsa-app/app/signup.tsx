import { StyleSheet, Text, View } from 'react-native'
import React, { useEffect } from 'react'
import Signup from '../components/onboarding/Signup'
import { useAuth } from '../AuthContext/AuthContext';
import { router } from 'expo-router';
//import SignupFlow from '@/components/signup/signup';
import SignupFlow from '@/components/signup/signupflow';

const signup = () => {
  const {isAuthenticated}=useAuth();
  useEffect(()=>{
    if(isAuthenticated){
      //@ts-ignore
      router.push('/home')
        }
  },[])
  return (
   /*  <Signup/> */
   <SignupFlow/>
  )
}

export default signup

const styles = StyleSheet.create({})