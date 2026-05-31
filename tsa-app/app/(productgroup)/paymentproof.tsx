// app/(productgroup)/paymentproof.tsx
//
// Payment-proof screen — hosts the PaymentProof uploader for a given order id.
// Restyled onto the TSA Signature design system (themed SafeAreaView + ScrollView
// shell) off the legacy bare-View wrapper. Routing/params are preserved verbatim:
// the order `id` from useLocalSearchParams is passed straight through to the
// PaymentProof component, which owns the upload + submit flow.
import React from 'react'
import { ScrollView } from 'react-native'
import { SafeAreaView } from 'react-native-safe-area-context'
import { useLocalSearchParams } from 'expo-router'
import { useTheme } from '@/theme'
import PaymentProof from '../../components/orders/paymentproof'

const PaymentProofRoute = () => {
  const { colors, spacing } = useTheme()
  const { id } = useLocalSearchParams()
  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <PaymentProof id={id} />
      </ScrollView>
    </SafeAreaView>
  )
}

export default PaymentProofRoute
