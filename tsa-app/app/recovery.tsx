
import { router } from 'expo-router';
import RecoveryScreen from '../screens/RecoveryScreen'
import { useEffect } from 'react';
import { useAuth } from '../AuthContext/AuthContext';

const RecoveryPage = () => {
  const {isAuthenticated}=useAuth();

  useEffect(()=>{
    if(isAuthenticated){
      //@ts-ignore
      router.push('/home')
        }
  },[])
  return (
   <RecoveryScreen/>
  )
}

export default RecoveryPage