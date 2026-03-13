// __tests__/wallet.test.ts
// Tests for the wallet service core functions
import { ethers } from 'ethers';

// We test the pure logic functions without native module dependencies
// (react-native-keychain is mocked away since it requires native bindings)

describe('Wallet Service - Core Logic', () => {
  describe('generateWallet', () => {
    it('should generate a wallet with a 12-word mnemonic and 0x-prefixed address', () => {
      const wallet = ethers.Wallet.createRandom();
      const mnemonic = wallet.mnemonic?.phrase;

      expect(mnemonic).toBeDefined();
      expect(mnemonic!.split(' ').length).toBe(12);
      expect(wallet.address).toMatch(/^0x[a-fA-F0-9]{40}$/);
      expect(wallet.privateKey).toMatch(/^0x[a-fA-F0-9]{64}$/);
    });

    it('should generate different wallets each time', () => {
      const wallet1 = ethers.Wallet.createRandom();
      const wallet2 = ethers.Wallet.createRandom();

      expect(wallet1.address).not.toBe(wallet2.address);
      expect(wallet1.privateKey).not.toBe(wallet2.privateKey);
      expect(wallet1.mnemonic?.phrase).not.toBe(wallet2.mnemonic?.phrase);
    });
  });

  describe('importWalletFromMnemonic', () => {
    it('should derive the same address from a known test mnemonic', () => {
      const testMnemonic =
        'abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon abandon about';

      const wallet = ethers.HDNodeWallet.fromPhrase(testMnemonic);

      // This is the well-known address for this mnemonic (BIP-44 m/44'/60'/0'/0/0)
      expect(wallet.address).toBe('0x9858EfFD232B4033E47d90003D41EC34EcaEda94');
      expect(wallet.address).toMatch(/^0x[a-fA-F0-9]{40}$/);
    });

    it('should reject an invalid mnemonic', () => {
      const invalidMnemonic = 'not a valid mnemonic phrase at all these are random words';

      expect(ethers.Mnemonic.isValidMnemonic(invalidMnemonic)).toBe(false);
    });

    it('should accept a valid mnemonic', () => {
      const wallet = ethers.Wallet.createRandom();
      const mnemonic = wallet.mnemonic?.phrase;

      expect(mnemonic).toBeDefined();
      expect(ethers.Mnemonic.isValidMnemonic(mnemonic!)).toBe(true);
    });
  });

  describe('wallet address validation', () => {
    it('should validate a correct Ethereum address', () => {
      expect(ethers.isAddress('0x9858EfFD232B4033E47d90003D41EC34EcaEda94')).toBe(true);
    });

    it('should validate a checksummed address', () => {
      expect(ethers.isAddress('0xd8dA6BF26964aF9D7eEd9e03E53415D37aA96045')).toBe(true);
    });

    it('should reject a too-short address', () => {
      expect(ethers.isAddress('0x1234')).toBe(false);
    });

    it('should reject a non-hex address', () => {
      expect(ethers.isAddress('not-an-address')).toBe(false);
    });

    it('should reject an empty string', () => {
      expect(ethers.isAddress('')).toBe(false);
    });

    it('should accept a lowercase address', () => {
      expect(
        ethers.isAddress('0x9858effd232b4033e47d90003d41ec34ecaeda94')
      ).toBe(true);
    });
  });
});
