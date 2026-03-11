
import React, { useEffect } from 'react'
import RecoveryOTP from '../components/onboarding/RecoveryOTP'
import { router } from 'expo-router';
import { useAuth } from '../AuthContext/AuthContext';

const PassWordRecovery = () => {
  const {isAuthenticated}=useAuth();

  useEffect(()=>{
    if(isAuthenticated){
      //@ts-ignore
      router.push('/home')
        }
  },[])
  return (
    <RecoveryOTP/>
  )
}

export default PassWordRecovery