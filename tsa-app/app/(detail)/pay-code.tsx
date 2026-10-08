// app/(detail)/pay-code.tsx
//
// TSA Pay by code: the payer types (or pastes) the TSA-XXXXX-XXXXX code a
// business's page or printed invoice shows, and lands on the ordinary checkout
// (/pay?id=…), which handles every status. Reached from the wallet's TSA Pay
// tile. The field has no maxLength on purpose: a pasted "TSA-…" code is
// fifteen characters and must arrive whole to be read.
import React, { useRef, useState } from 'react';
import { ScrollView } from 'react-native';
import * as Clipboard from 'expo-clipboard';
import { router } from 'expo-router';
import { useTheme } from '@/theme';
import { Button, Input, Text } from '@/components/ui';
import { lookupPayCode } from '@/services/payApi';
import { dropTypedPrefix, normalizePayCode, payCodeFieldText, payCodeLookupError } from '@/utils/payCode';

export default function PayCodeScreen() {
  const { colors, spacing } = useTheme();
  const [text, setText] = useState('');
  const [error, setError] = useState<string | undefined>();
  const [busy, setBusy] = useState(false);
  const code = normalizePayCode(text);
  // `busy` is state, so two calls in one frame both see it false: this ref is the real guard.
  const inFlight = useRef(false);

  const edit = (value: string) => {
    setText(payCodeFieldText(dropTypedPrefix(value)));
    setError(undefined);
  };

  const paste = async () => {
    try {
      edit(await Clipboard.getStringAsync());
    } catch {
      // Nothing readable on the clipboard: the field stays as it was.
    }
  };

  const submit = async () => {
    if (!code || inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError(undefined);
    try {
      const id = await lookupPayCode(code);
      router.push(`/pay?id=${id}` as any);
    } catch (err) {
      setError(payCodeLookupError(err));
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  };

  return (
    <ScrollView
      style={{ flex: 1, backgroundColor: colors.background }}
      contentContainerStyle={{ padding: spacing.lg, gap: spacing.lg }}
      keyboardShouldPersistTaps="handled"
    >
      <Text variant="bodySm" color="textSecondary">
        Enter the payment code the business gave you. It looks like TSA-7K2QF-M9XWD.
      </Text>
      <Input
        label="Payment code"
        value={text}
        onChangeText={edit}
        placeholder="XXXXX-XXXXX"
        autoCapitalize="characters"
        autoCorrect={false}
        autoComplete="off"
        returnKeyType="go"
        onSubmitEditing={() => void submit()}
        error={error}
        leftAdornment={<Text variant="body" color="textSecondary">TSA-</Text>}
        rightAdornment={<Button title="Paste" variant="ghost" size="sm" onPress={paste} />}
      />
      <Button title="Continue" size="lg" onPress={submit} loading={busy} disabled={!code || busy} />
    </ScrollView>
  );
}
