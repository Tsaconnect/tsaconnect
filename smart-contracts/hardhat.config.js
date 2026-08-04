require("@nomicfoundation/hardhat-toolbox");
require("@openzeppelin/hardhat-upgrades");
require("dotenv").config();

// Accept the deployer key with or without a leading 0x (hardhat needs 0x-prefixed).
const RAW_PK = process.env.DEPLOYER_PRIVATE_KEY;
const ACCOUNTS = RAW_PK ? [RAW_PK.startsWith("0x") ? RAW_PK : "0x" + RAW_PK] : [];

/** @type import('hardhat/config').HardhatUserConfig */
module.exports = {
  solidity: {
    compilers: [
      {
        version: "0.8.24",
        settings: {
          optimizer: {
            enabled: true,
            runs: 200,
          },
          // OpenZeppelin v5 deps (used by P2PEscrowPool) emit Cancun opcodes
          // (mcopy/tstore). Sonic's EVM supports Cancun on both mainnet and
          // testnet, so this is safe.
          evmVersion: "cancun",
        },
      },
    ],
    // NOTE: P2PEscrowV3 previously needed a per-file `runs: 1` optimizer override to fit
    // under the 24576-byte EIP-170 limit (it landed 12 bytes under). After converting its
    // revert strings to custom errors (audit finding #3) the deployed bytecode dropped to
    // ~23.2KB at the standard runs=200 profile (~1.3KB headroom), so the override was
    // removed: V3 now shares the SAME optimizer profile as every other contract, which
    // also eliminates the runs=1 runtime-gas penalty on every openTrade/release.
  },
  networks: {
    // Local fork of a live chain, for rehearsing migrations/upgrades against
    // real state before touching mainnet. Inert unless FORK_RPC is set, so
    // ordinary `hardhat test` runs are unaffected.
    hardhat: process.env.FORK_RPC
      ? {
          forking: {
            url: process.env.FORK_RPC,
            ...(process.env.FORK_BLOCK ? { blockNumber: Number(process.env.FORK_BLOCK) } : {}),
          },
          // Sonic is not in Hardhat's built-in hardfork history, so forked
          // historical calls fail without an explicit activation table. The key
          // must be a plain number and the value a hardfork→block map.
          hardfork: "cancun",
          chains: {
            146: { hardforkHistory: { shanghai: 0, cancun: 1 } },
          },
        }
      : {},
    sonicTestnet: {
      url: process.env.SONIC_TESTNET_RPC || "https://rpc.testnet.soniclabs.com",
      chainId: 14601,
      accounts: ACCOUNTS,
    },
    sonicMainnet: {
      url: process.env.SONIC_MAINNET_RPC || "https://rpc.soniclabs.com",
      chainId: 146,
      accounts: ACCOUNTS,
    },
    bscTestnet: {
      url: process.env.BSC_TESTNET_RPC || "https://data-seed-prebsc-1-s1.binance.org:8545",
      chainId: 97,
      accounts: ACCOUNTS,
    },
    bscMainnet: {
      url: process.env.BSC_MAINNET_RPC || "https://bsc-dataseed.binance.org",
      chainId: 56,
      accounts: ACCOUNTS,
    },
  },
  etherscan: {
    apiKey: {
      sonicMainnet: process.env.SONICSCAN_API_KEY || "",
      sonicTestnet: process.env.SONICSCAN_API_KEY || "",
      bsc: process.env.BSCSCAN_API_KEY || "",
      bscTestnet: process.env.BSCSCAN_API_KEY || "",
    },
    customChains: [
      {
        network: "sonicMainnet",
        chainId: 146,
        urls: {
          apiURL: "https://api.sonicscan.org/api",
          browserURL: "https://sonicscan.org",
        },
      },
      {
        network: "sonicTestnet",
        chainId: 14601,
        urls: {
          apiURL: "https://api-testnet.sonicscan.org/api",
          browserURL: "https://testnet.sonicscan.org",
        },
      },
      {
        network: "bscTestnet",
        chainId: 97,
        urls: {
          apiURL: "https://api-testnet.bscscan.com/api",
          browserURL: "https://testnet.bscscan.com",
        },
      },
    ],
  },
  sourcify: {
    enabled: false,
  },
};
