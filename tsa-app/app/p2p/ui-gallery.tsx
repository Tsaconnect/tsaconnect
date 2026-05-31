// TSA Signature — kitchen-sink gallery for visual QA / screenshots. Dev-only;
// reachable at /p2p/ui-gallery. Renders every primitive in both schemes.
import React, { useState } from 'react';
import { ScrollView, View } from 'react-native';
import { SafeAreaView } from 'react-native-safe-area-context';
import { useTheme, useThemeControls } from '@/theme';
import {
  AccentCard,
  AmountInput,
  AmountText,
  Avatar,
  Badge,
  BottomActionBar,
  Button,
  Card,
  Chip,
  CopyableField,
  CountdownBar,
  Divider,
  EmptyState,
  ErrorState,
  IconButton,
  Input,
  PickerSheet,
  RadioGroup,
  SegmentedControl,
  SelectRow,
  Sheet,
  Skeleton,
  SkeletonGroup,
  Spinner,
  StatusPill,
  SummaryCard,
  SummaryRow,
  Text,
  Toggle,
  TrustBadge,
  useToast,
} from '@/components/ui';

const Section: React.FC<{ title: string; children: React.ReactNode }> = ({ title, children }) => {
  const { spacing } = useTheme();
  return (
    <View style={{ gap: spacing.sm, marginBottom: spacing.xl }}>
      <Text variant="overline" color="textMuted">
        {title.toUpperCase()}
      </Text>
      {children}
    </View>
  );
};

const Row: React.FC<{ children: React.ReactNode }> = ({ children }) => {
  const { spacing } = useTheme();
  return <View style={{ flexDirection: 'row', flexWrap: 'wrap', gap: spacing.sm, alignItems: 'center' }}>{children}</View>;
};

export default function UIGallery() {
  const { colors, spacing } = useTheme();
  const { setScheme } = useThemeControls();
  const toast = useToast();
  const [amount, setAmount] = useState('');
  const [text, setText] = useState('');
  const [seg, setSeg] = useState('buy');
  const [toggle, setToggle] = useState(true);
  const [radio, setRadio] = useState<string | null>('bank');
  const [sheet, setSheet] = useState(false);
  const [picker, setPicker] = useState(false);
  const [bank, setBank] = useState<string | undefined>();

  return (
    <SafeAreaView style={{ flex: 1, backgroundColor: colors.background }} edges={['top']}>
      <ScrollView contentContainerStyle={{ padding: spacing.lg, paddingBottom: spacing['4xl'] }}>
        <Text variant="title1" weight="700">
          TSA Signature
        </Text>
        <Text variant="bodySm" color="textSecondary" style={{ marginBottom: spacing.lg }}>
          Design-system gallery
        </Text>

        <Section title="Scheme">
          <Row>
            <Button title="Light" size="sm" variant="secondary" fullWidth={false} onPress={() => setScheme('light')} />
            <Button title="Dark" size="sm" variant="secondary" fullWidth={false} onPress={() => setScheme('dark')} />
            <Button title="System" size="sm" variant="ghost" fullWidth={false} onPress={() => setScheme('system')} />
          </Row>
        </Section>

        <Section title="Typography">
          <Text variant="display">Display 34</Text>
          <Text variant="title1">Title 1</Text>
          <Text variant="title3">Title 3</Text>
          <Text variant="body">Body — the quick brown fox.</Text>
          <Text variant="caption" color="textMuted">
            Caption muted
          </Text>
          <AmountText value={1498.5} currency="NGN" variant="display" />
        </Section>

        <Section title="Buttons">
          <Button title="Primary" onPress={() => toast.success('Primary tapped')} leftIcon="bolt" />
          <Row>
            <Button title="Secondary" variant="secondary" fullWidth={false} />
            <Button title="Ghost" variant="ghost" fullWidth={false} />
          </Row>
          <Row>
            <Button title="Danger" variant="danger" fullWidth={false} />
            <Button title="Success" variant="success" fullWidth={false} />
            <Button title="Loading" loading fullWidth={false} />
          </Row>
          <Row>
            <IconButton icon="copy" accessibilityLabel="copy" variant="surface" />
            <IconButton icon="refresh" accessibilityLabel="refresh" variant="surface" />
            <IconButton icon="dispute" accessibilityLabel="dispute" color={colors.danger} variant="surface" />
          </Row>
        </Section>

        <Section title="Status pills (namespace-aware)">
          <Row>
            <StatusPill kind="trade" status="pending_payment" />
            <StatusPill kind="trade" status="paid" />
            <StatusPill kind="trade" status="releasing" />
            <StatusPill kind="trade" status="released" />
            <StatusPill kind="trade" status="disputed" />
            <StatusPill kind="trade" status="expired" />
          </Row>
          <Row>
            <StatusPill kind="ad" status="active" />
            <StatusPill kind="ad" status="paused" />
            <StatusPill kind="ad" status="rateStale" />
            <StatusPill kind="dispute" status="open" />
            <StatusPill kind="dispute" status="resolved" />
          </Row>
          <Row>
            <TrustBadge tier="trusted" autoAccept />
            <TrustBadge tier="verified" />
            <TrustBadge tier="new" />
            <Badge label="0.5% fee" icon="info" />
          </Row>
        </Section>

        <Section title="Selection">
          <SegmentedControl
            segments={[
              { key: 'buy', label: 'Buy' },
              { key: 'sell', label: 'Sell' },
            ]}
            value={seg}
            onChange={setSeg}
          />
          <Row>
            <Chip label="All methods" selected={!radio} onPress={() => setRadio(null)} leftIcon="filter" />
            <Chip label="Bank" selected={radio === 'bank'} onPress={() => setRadio('bank')} leftIcon="bank" />
            <Chip label="OPay" selected={radio === 'opay'} onPress={() => setRadio('opay')} leftIcon="mobile" />
          </Row>
          <View style={{ flexDirection: 'row', alignItems: 'center', justifyContent: 'space-between' }}>
            <Text variant="body">Auto-release ⚡</Text>
            <Toggle value={toggle} onValueChange={setToggle} accessibilityLabel="auto-release" />
          </View>
          <RadioGroup
            options={[
              { value: 'bank', label: 'Bank transfer', sublabel: '~10 min', icon: 'bank' },
              { value: 'opay', label: 'OPay', sublabel: 'Instant', icon: 'mobile' },
            ]}
            value={radio}
            onChange={setRadio}
          />
        </Section>

        <Section title="Inputs">
          <Input label="Account name" placeholder="e.g. Jane Doe" value={text} onChangeText={setText} counter={40} maxLength={40} helper="As shown on your bank statement" />
          <AmountInput label="Amount" value={amount} onChangeText={setAmount} symbol="₦" onMax={() => setAmount('150000')} helper="Balance ₦150,000" />
          <SelectRow label="Bank" value={bank} placeholder="Select bank" leftIcon="bank" onPress={() => setPicker(true)} />
        </Section>

        <Section title="Cards">
          <AccentCard>
            <View style={{ flexDirection: 'row', justifyContent: 'space-between' }}>
              <View style={{ flexDirection: 'row', gap: spacing.sm, alignItems: 'center' }}>
                <Avatar name="Crypto King" online />
                <View>
                  <Text variant="bodyMed" weight="700">
                    CryptoKing
                  </Text>
                  <TrustBadge tier="trusted" autoAccept />
                </View>
              </View>
              <Button title="Buy" size="sm" fullWidth={false} rightIcon="arrowRight" />
            </View>
            <Divider spacing="sm" />
            <AmountText value={1498.5} currency="NGN" variant="title2" />
            <Text variant="caption" color="textSecondary">
              ₦5k – ₦2M · ~2 min
            </Text>
          </AccentCard>
          <Card>
            <SummaryCard dividerBeforeLast>
              <SummaryRow label="You pay" value="₦15,000" />
              <SummaryRow label="Fee (0.5%)" value="₦75" valueColor="textSecondary" />
              <SummaryRow label="You receive" value="9.95 USDT" total />
            </SummaryCard>
          </Card>
          <Card>
            <CountdownBar expiresAt={Date.now() + 4 * 60 * 1000} totalMs={30 * 60 * 1000} />
          </Card>
          <CopyableField label="Account number" value="0123456789" />
        </Section>

        <Section title="Feedback">
          <Row>
            <Spinner label="Matching merchants…" inline />
          </Row>
          <Skeleton height={18} width="70%" />
          <SkeletonGroup shape="adCard" count={1} />
          <Row>
            <Button title="Toast success" size="sm" variant="secondary" fullWidth={false} onPress={() => toast.success('Trade released')} />
            <Button title="Toast error" size="sm" variant="secondary" fullWidth={false} onPress={() => toast.error('Network error')} />
          </Row>
          <Card>
            <EmptyState title="No active trades" subtitle="Your open trades will show up here." icon="empty" actionLabel="Browse market" onAction={() => {}} />
          </Card>
          <Card>
            <ErrorState subtitle="Couldn’t load ads." onRetry={() => toast.info('Retrying…')} />
          </Card>
        </Section>

        <Section title="Overlays">
          <Button title="Open sheet" variant="secondary" onPress={() => setSheet(true)} />
        </Section>
      </ScrollView>

      <BottomActionBar>
        <Button title="Docked CTA" rightIcon="arrowRight" onPress={() => toast.info('Docked CTA')} />
      </BottomActionBar>

      <Sheet visible={sheet} onClose={() => setSheet(false)} title="Example sheet">
        <View style={{ padding: spacing.lg, gap: spacing.md }}>
          <Text variant="body" color="textSecondary">
            A bottom sheet with scrim + close. Used for pickers and confirmations.
          </Text>
          <Button title="Got it" onPress={() => setSheet(false)} />
        </View>
      </Sheet>

      <PickerSheet
        visible={picker}
        onClose={() => setPicker(false)}
        title="Select bank"
        selectedId={bank}
        items={[
          { id: 'gtb', label: 'GTBank', sublabel: '044', icon: 'bank', group: 'Popular' },
          { id: 'access', label: 'Access Bank', sublabel: '044', icon: 'bank', group: 'Popular' },
          { id: 'kuda', label: 'Kuda', sublabel: 'Digital', icon: 'mobile', group: 'Digital' },
        ]}
        onSelect={(it) => {
          setBank(it.label);
          setPicker(false);
        }}
      />
    </SafeAreaView>
  );
}
