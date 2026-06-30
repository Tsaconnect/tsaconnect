# Multi-chain Deploy Tooling Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Make all latest-version contracts deployable to Sonic or BSC by selecting the Hardhat network, from one config-driven deploy system that replaces the scattered bespoke scripts.

**Architecture:** A single per-chain address config (`deploy/chains.js`), a shared library (`deploy/lib.js`), and one testable deploy module per contract (`deploy/<contract>.js`) exporting `deploy(hre, opts)`. Each module deploys the latest version and runs its post-deploy wiring. Verification is in-process (Hardhat network + mock ERC-20s) — no real-chain transactions.

**Tech Stack:** Hardhat 2.28, `@openzeppelin/hardhat-upgrades` 3.9 (UUPS proxies), ethers v6 (via Hardhat), mocha + chai (`hardhat test`).

## Global Constraints

- Tooling only — **no on-chain transactions** are broadcast by this work.
- Contracts in scope (latest of each family): `P2PEscrowV3`, `ProductEscrowV2`, `ServiceContactV2`, `FeeManager`, `MCGPRewardDistributor`. No contract source changes.
- Networks (Hardhat network names): `sonicMainnet`, `sonicTestnet`, `bscMainnet`, `bscTestnet`.
- `MCGP` on BSC is `null`; any script needing it must **fail fast** with a network-qualified error, never deploy partially.
- Canonical Permit2 (BSC + most chains): `0x000000000022D473030F116dDEE9F6B43aC78BA3`.
- BSC-mainnet tokens: USDT `0x55d398326f99059fF775485246999027B3197955`, USDC `0x8AC76a51cc950d9822D68b83FE1Ad97B32Cd580d`.
- Each deploy module exports `async function deploy(hre, opts)` returning the deployed addresses, with a CLI entrypoint guarded by `if (require.main === module)`. This is the testable seam: tests call `deploy(hre, { chain, feeManager })` with an explicit config override.
- Initializer / setter signatures (verbatim from the contracts):
  - `P2PEscrowV3.initialize(address initialOwner, address[] initialAdmins)`; setters `setSystemWallet(address)`, `setTokenAcceptance(address,bool)`, `setTokenSymbol(address,string)`, `setMcgpToken(address)`, `setPermit2(address)`, `setTrustedForwarder(address)`, `setFeeManager(address)`.
  - `ProductEscrowV2.initialize(address initialOwner, address[] initialAdmins)`; setters `setMcgpToken(address)`, `setTokenAcceptance(address,bool)`, `setTokenSymbol(address,string)`, `setFeeManager(address)`.
  - `ServiceContactV2.initialize(address initialOwner)`; setters `setTokenAcceptance(address,bool)`, `setFeeAmount(address,uint256)`, `setFeeManager(address)`, `setTokenSymbol(address,string)`.
  - `FeeManager.initialize(address initialOwner)` (UUPS).
  - `MCGPRewardDistributor` constructor `(address mcgpToken, address initialOwner)` — non-proxy.

---

## File Structure

- `deploy/chains.js` — per-chain address config + `CANONICAL_PERMIT2`.
- `deploy/lib.js` — `resolveChain`, `requireAddr`, `readLedger`, `writeLedger`, `wireFeeManager`, `acceptTokens`.
- `deploy/feemanager.js` — deploy FeeManager proxy.
- `deploy/p2p-escrow.js` — deploy P2PEscrowV3 proxy + wiring.
- `deploy/product-escrow.js` — deploy ProductEscrowV2 proxy + wiring.
- `deploy/service-contact.js` — deploy ServiceContactV2 proxy + wiring.
- `deploy/mcgp-distributor.js` — deploy MCGPRewardDistributor (non-proxy).
- `deploy/upgrade.js` — upgrade an existing proxy to latest impl.
- `deploy/all.js` — orchestrate fresh-chain deploy in dependency order.
- `test/deploy/helpers.js` — mock-token + override-config test rig.
- `test/deploy/*.test.js` — per-module dry-run tests + lib unit tests.
- `deployments/<network>.json` — address ledger (generated; git-ignored except a `.gitkeep`).

---

## Task 1: Config (`deploy/chains.js`) + completeness test

**Files:**
- Create: `smart-contracts/deploy/chains.js`
- Test: `smart-contracts/test/deploy/chains.test.js`

**Interfaces:**
- Produces: `module.exports = { CANONICAL_PERMIT2: string, chains: { [network]: ChainCfg } }` where `ChainCfg = { tokens: { USDT, USDC, MCGP }, permit2, forwarder, owner, admins: string[], systemWallet }`. Fields that are unknown are `null`.

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/chains.test.js
const { expect } = require("chai");
const { chains, CANONICAL_PERMIT2 } = require("../../deploy/chains");

describe("deploy/chains config", () => {
  const REQUIRED = ["sonicMainnet", "sonicTestnet", "bscMainnet", "bscTestnet"];

  it("defines all four networks", () => {
    for (const n of REQUIRED) expect(chains[n], n).to.be.an("object");
  });

  it("has USDT/USDC for every network", () => {
    for (const n of REQUIRED) {
      expect(chains[n].tokens.USDT, `${n} USDT`).to.match(/^0x[0-9a-fA-F]{40}$/);
      expect(chains[n].tokens.USDC, `${n} USDC`).to.match(/^0x[0-9a-fA-F]{40}$/);
    }
  });

  it("has MCGP on Sonic but null on BSC", () => {
    expect(chains.sonicMainnet.tokens.MCGP).to.match(/^0x[0-9a-fA-F]{40}$/);
    expect(chains.sonicTestnet.tokens.MCGP).to.match(/^0x[0-9a-fA-F]{40}$/);
    expect(chains.bscMainnet.tokens.MCGP).to.equal(null);
    expect(chains.bscTestnet.tokens.MCGP).to.equal(null);
  });

  it("uses canonical Permit2 on BSC", () => {
    expect(chains.bscMainnet.permit2).to.equal(CANONICAL_PERMIT2);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/chains.test.js`
Expected: FAIL — `Cannot find module '../../deploy/chains'`.

- [ ] **Step 3: Write `deploy/chains.js`**

Migrate the known addresses from the current scripts/`.env.example` into this file. Read `scripts/deploy-fresh-escrows-v2-testnet.js`, `scripts/wire-bsc-p2p-escrow.js`, `scripts/deploy-testnet-upgradeable.js`, and `.env.example` for the exact Sonic-mainnet/testnet and BSC-testnet token addresses; copy them in verbatim. Use `process.env.<NET>_<TOKEN>_ADDRESS` overrides where the old scripts did, with the migrated address as the default.

```js
// smart-contracts/deploy/chains.js
const CANONICAL_PERMIT2 = "0x000000000022D473030F116dDEE9F6B43aC78BA3";

// Public on-chain addresses only — deployer keys stay in .env.
// Defaults migrated from the legacy scripts; env vars still override.
const chains = {
  sonicMainnet: {
    tokens: {
      USDT: process.env.MAINNET_USDT_ADDRESS || "<SONIC_MAINNET_USDT>",
      USDC: process.env.MAINNET_USDC_ADDRESS || "<SONIC_MAINNET_USDC>",
      MCGP: process.env.MAINNET_MCGP_ADDRESS || "<SONIC_MAINNET_MCGP>",
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,        // null → deployer at runtime
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
  },
  sonicTestnet: {
    tokens: {
      USDT: process.env.TESTNET_USDT_ADDRESS || "0xb5617cD43dcd27C9E0be8c0FF82F9406Cb1123ad",
      USDC: process.env.TESTNET_USDC_ADDRESS || "0x29219dd400f2Bf60E5a23d13Be72B486D4038894",
      MCGP: process.env.TESTNET_MCGP_ADDRESS || "0x517600323e5E2938207fA2e2e915B9D80e5B2b21",
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
  },
  bscMainnet: {
    tokens: {
      USDT: process.env.BSC_MAINNET_USDT_ADDRESS || "0x55d398326f99059fF775485246999027B3197955",
      USDC: process.env.BSC_MAINNET_USDC_ADDRESS || "0x8AC76a51cc950d9822D68b83FE1Ad97B32Cd580d",
      MCGP: null, // no MCGP on BSC yet
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
  },
  bscTestnet: {
    tokens: {
      USDT: process.env.BSC_TESTNET_USDT_ADDRESS || "0xF0EE975DB8BbD79f3e8346f6304599061E4f32A7",
      USDC: process.env.BSC_TESTNET_USDC_ADDRESS || "0x6c96B6EB227D1254247cD5015Bfc3e8Ade94415d",
      MCGP: null,
    },
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: process.env.OWNER_ADDRESS || null,
    admins: (process.env.ADMIN_ADDRESSES || "").split(",").filter(Boolean),
    systemWallet: process.env.SYSTEM_WALLET_ADDRESS || null,
  },
};

module.exports = { chains, CANONICAL_PERMIT2 };
```

Replace each `<SONIC_MAINNET_*>` placeholder with the real address found in the legacy scripts/`.env`. If a Sonic-mainnet token address cannot be found in the repo, leave the `process.env...||` with NO default and set the literal to `null`, then adjust the test for that one field to allow `null` — do not invent an address.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/chains.test.js`
Expected: PASS (4 passing).

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/chains.js smart-contracts/test/deploy/chains.test.js
git commit -m "feat(deploy): per-chain address config for Sonic + BSC"
```

---

## Task 2: Library (`deploy/lib.js`)

**Files:**
- Create: `smart-contracts/deploy/lib.js`
- Test: `smart-contracts/test/deploy/lib.test.js`

**Interfaces:**
- Consumes: `chains` from `deploy/chains.js`.
- Produces:
  - `resolveChain(hre)` → `ChainCfg` for `hre.network.name`; throws `"deploy: no chain config for network '<name>'"`.
  - `requireAddr(cfg, label, value)` → returns `value` or throws `"deploy: ${label} not configured for this network"`.
  - `readLedger(network)` → object (or `{}` if file absent).
  - `writeLedger(network, key, addresses)` → merges `{ [key]: addresses }` into `deployments/<network>.json`.

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/lib.test.js
const { expect } = require("chai");
const fs = require("fs");
const path = require("path");
const { resolveChain, requireAddr, readLedger, writeLedger } = require("../../deploy/lib");

describe("deploy/lib", () => {
  it("resolveChain returns the config for a known network", () => {
    const cfg = resolveChain({ network: { name: "sonicTestnet" } });
    expect(cfg.tokens.USDC).to.match(/^0x/);
  });

  it("resolveChain throws on an unknown network", () => {
    expect(() => resolveChain({ network: { name: "nope" } })).to.throw(/no chain config/);
  });

  it("requireAddr returns a present value and throws on null", () => {
    expect(requireAddr({}, "MCGP", "0xabc")).to.equal("0xabc");
    expect(() => requireAddr({}, "MCGP", null)).to.throw(/MCGP not configured/);
  });

  it("writeLedger then readLedger round-trips", () => {
    const net = "hardhat-libtest";
    const file = path.join(__dirname, "../../deployments", `${net}.json`);
    if (fs.existsSync(file)) fs.unlinkSync(file);
    writeLedger(net, "FeeManager", { proxy: "0x1", impl: "0x2" });
    expect(readLedger(net).FeeManager.proxy).to.equal("0x1");
    fs.unlinkSync(file);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/lib.test.js`
Expected: FAIL — `Cannot find module '../../deploy/lib'`.

- [ ] **Step 3: Write `deploy/lib.js`**

```js
// smart-contracts/deploy/lib.js
const fs = require("fs");
const path = require("path");
const { chains } = require("./chains");

const LEDGER_DIR = path.join(__dirname, "..", "deployments");

function resolveChain(hre) {
  const name = hre.network.name;
  const cfg = chains[name];
  if (!cfg) throw new Error(`deploy: no chain config for network '${name}'`);
  return cfg;
}

function requireAddr(cfg, label, value) {
  if (!value) throw new Error(`deploy: ${label} not configured for this network`);
  return value;
}

function ledgerPath(network) {
  return path.join(LEDGER_DIR, `${network}.json`);
}

function readLedger(network) {
  const p = ledgerPath(network);
  return fs.existsSync(p) ? JSON.parse(fs.readFileSync(p, "utf8")) : {};
}

function writeLedger(network, key, addresses) {
  if (!fs.existsSync(LEDGER_DIR)) fs.mkdirSync(LEDGER_DIR, { recursive: true });
  const ledger = readLedger(network);
  ledger[key] = { ...addresses, updatedAt: new Date().toISOString() };
  fs.writeFileSync(ledgerPath(network), JSON.stringify(ledger, null, 2) + "\n");
}

module.exports = { resolveChain, requireAddr, readLedger, writeLedger };
```

Note: `new Date()` is fine here (real Node script, not a workflow sandbox).

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/lib.test.js`
Expected: PASS (4 passing).

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/lib.js smart-contracts/test/deploy/lib.test.js
git commit -m "feat(deploy): shared lib — resolveChain, requireAddr, ledger"
```

---

## Task 3: Test rig (`test/deploy/helpers.js`)

**Files:**
- Create: `smart-contracts/test/deploy/helpers.js`

**Interfaces:**
- Produces:
  - `async function deployMockTokens(decimals = 18)` → `{ USDT, USDC, MCGP }` of deployed `MockERC20` addresses.
  - `async function testChain(overrides = {})` → a `ChainCfg` whose addresses are freshly deployed mocks and whose `owner`/`admins`/`systemWallet` are the first local signer, deep-merged with `overrides`.

- [ ] **Step 1: Write the helper (no separate test — exercised by Tasks 4-8)**

Read `contracts/mocks/MockERC20.sol` first to confirm its constructor signature, then match it below (adjust args if the constructor differs).

```js
// smart-contracts/test/deploy/helpers.js
const { ethers } = require("hardhat");
const { CANONICAL_PERMIT2 } = require("../../deploy/chains");

async function deployMockTokens(decimals = 18) {
  const Mock = await ethers.getContractFactory("MockERC20");
  const mk = async (name, sym) => {
    const t = await Mock.deploy(name, sym, decimals); // adjust to MockERC20's real ctor
    await t.waitForDeployment();
    return await t.getAddress();
  };
  return { USDT: await mk("Mock USDT", "USDT"), USDC: await mk("Mock USDC", "USDC"), MCGP: await mk("Mock MCGP", "MCGP") };
}

async function testChain(overrides = {}) {
  const [signer] = await ethers.getSigners();
  const me = await signer.getAddress();
  const tokens = await deployMockTokens();
  const base = {
    tokens,
    permit2: CANONICAL_PERMIT2,
    forwarder: null,
    owner: me,
    admins: [me],
    systemWallet: me,
  };
  return { ...base, ...overrides, tokens: { ...tokens, ...(overrides.tokens || {}) } };
}

module.exports = { deployMockTokens, testChain };
```

- [ ] **Step 2: Commit**

```bash
git add smart-contracts/test/deploy/helpers.js
git commit -m "test(deploy): mock-token + override-config rig"
```

---

## Task 4: `deploy/feemanager.js`

**Files:**
- Create: `smart-contracts/deploy/feemanager.js`
- Test: `smart-contracts/test/deploy/feemanager.test.js`

**Interfaces:**
- Consumes: `resolveChain`, `writeLedger` (lib); `testChain` (helpers).
- Produces: `async function deploy(hre, { chain } = {})` → `{ proxy, impl }`. Module also exports a CLI entrypoint.

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/feemanager.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/feemanager");

describe("deploy/feemanager", () => {
  it("deploys a FeeManager proxy owned by the configured owner", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const fm = await hre.ethers.getContractAt("FeeManager", proxy);
    expect(await fm.owner()).to.equal(chain.owner);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/feemanager.test.js`
Expected: FAIL — `Cannot find module '../../deploy/feemanager'`.

- [ ] **Step 3: Write `deploy/feemanager.js`**

```js
// smart-contracts/deploy/feemanager.js
const { resolveChain, writeLedger } = require("./lib");

async function deploy(hre, { chain } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());

  const FeeManager = await ethers.getContractFactory("FeeManager");
  const fm = await upgrades.deployProxy(FeeManager, [owner], { kind: "uups" });
  await fm.waitForDeployment();

  const proxy = await fm.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "FeeManager", { proxy, impl });
  console.log(`FeeManager proxy=${proxy} impl=${impl} owner=${owner}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/feemanager.test.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/feemanager.js smart-contracts/test/deploy/feemanager.test.js
git commit -m "feat(deploy): unified FeeManager deploy module"
```

---

## Task 5: `deploy/p2p-escrow.js`

**Files:**
- Create: `smart-contracts/deploy/p2p-escrow.js`
- Test: `smart-contracts/test/deploy/p2p-escrow.test.js`

**Interfaces:**
- Consumes: `resolveChain`, `requireAddr`, `writeLedger`, `readLedger` (lib); `testChain` (helpers).
- Produces: `async function deploy(hre, { chain, feeManager } = {})` → `{ proxy, impl }`.

Before writing, read `scripts/deploy-p2p-escrow-v2.js` and `scripts/deploy-bsc-testnet-p2p-v2-bundle.js` to confirm the wiring order is preserved (system wallet, token acceptance, MCGP skip on BSC).

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/p2p-escrow.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/p2p-escrow");

describe("deploy/p2p-escrow", () => {
  it("deploys P2PEscrowV3, wires system wallet + tokens, sets MCGP when present", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", proxy);
    expect(await esc.owner()).to.equal(chain.owner);
    expect(await esc.systemWallet()).to.equal(chain.systemWallet);
    expect(await esc.acceptedTokens(chain.tokens.USDC)).to.equal(true);
    expect(await esc.mcgpToken()).to.equal(chain.tokens.MCGP);
  });

  it("skips MCGP wiring when MCGP is null (BSC)", async () => {
    const chain = await testChain({ tokens: { MCGP: null } });
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", proxy);
    expect(await esc.mcgpToken()).to.equal(hre.ethers.ZeroAddress);
  });
});
```

Note: confirm the public getter names (`systemWallet`, `acceptedTokens`, `mcgpToken`) against `P2PEscrowV3.sol`; adjust the assertions to the real getters if they differ.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/p2p-escrow.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `deploy/p2p-escrow.js`**

```js
// smart-contracts/deploy/p2p-escrow.js
const { resolveChain, writeLedger, readLedger } = require("./lib");

async function deploy(hre, { chain, feeManager } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());
  const admins = cfg.admins && cfg.admins.length ? cfg.admins : [owner];

  const Factory = await ethers.getContractFactory("P2PEscrowV3");
  const esc = await upgrades.deployProxy(Factory, [owner, admins], { kind: "uups" });
  await esc.waitForDeployment();
  const proxy = await esc.getAddress();

  if (cfg.systemWallet) await (await esc.setSystemWallet(cfg.systemWallet)).wait();
  for (const [sym, addr] of Object.entries(cfg.tokens)) {
    if (!addr) continue; // e.g. MCGP null on BSC
    await (await esc.setTokenAcceptance(addr, true)).wait();
    await (await esc.setTokenSymbol(addr, sym)).wait();
  }
  if (cfg.tokens.MCGP) await (await esc.setMcgpToken(cfg.tokens.MCGP)).wait();
  if (cfg.permit2) await (await esc.setPermit2(cfg.permit2)).wait();
  if (cfg.forwarder) await (await esc.setTrustedForwarder(cfg.forwarder)).wait();

  const fm = feeManager || (chain ? null : readLedger(network.name).FeeManager?.proxy);
  if (fm) await (await esc.setFeeManager(fm)).wait();

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "P2PEscrowV3", { proxy, impl });
  console.log(`P2PEscrowV3 proxy=${proxy} impl=${impl}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/p2p-escrow.test.js`
Expected: PASS (2 passing).

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/p2p-escrow.js smart-contracts/test/deploy/p2p-escrow.test.js
git commit -m "feat(deploy): unified P2PEscrowV3 deploy module"
```

---

## Task 6: `deploy/product-escrow.js`

**Files:**
- Create: `smart-contracts/deploy/product-escrow.js`
- Test: `smart-contracts/test/deploy/product-escrow.test.js`

**Interfaces:**
- Produces: `async function deploy(hre, { chain, feeManager } = {})` → `{ proxy, impl }`.

Read `scripts/deploy-mainnet-escrow.js` and `scripts/deploy-fresh-escrows-v2-testnet.js` to preserve the wiring (MCGP token, token acceptance, FeeManager link).

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/product-escrow.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/product-escrow");

describe("deploy/product-escrow", () => {
  it("deploys ProductEscrowV2 and accepts configured tokens", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const esc = await hre.ethers.getContractAt("ProductEscrowV2", proxy);
    expect(await esc.owner()).to.equal(chain.owner);
    expect(await esc.acceptedTokens(chain.tokens.USDT)).to.equal(true);
  });
});
```

Confirm the `acceptedTokens` getter name against `ProductEscrowV2.sol`; adjust if different.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/product-escrow.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `deploy/product-escrow.js`**

```js
// smart-contracts/deploy/product-escrow.js
const { resolveChain, writeLedger, readLedger } = require("./lib");

async function deploy(hre, { chain, feeManager } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());
  const admins = cfg.admins && cfg.admins.length ? cfg.admins : [owner];

  const Factory = await ethers.getContractFactory("ProductEscrowV2");
  const esc = await upgrades.deployProxy(Factory, [owner, admins], { kind: "uups" });
  await esc.waitForDeployment();
  const proxy = await esc.getAddress();

  if (cfg.tokens.MCGP) await (await esc.setMcgpToken(cfg.tokens.MCGP)).wait();
  for (const [sym, addr] of Object.entries(cfg.tokens)) {
    if (!addr) continue;
    await (await esc.setTokenAcceptance(addr, true)).wait();
    await (await esc.setTokenSymbol(addr, sym)).wait();
  }
  const fm = feeManager || (chain ? null : readLedger(network.name).FeeManager?.proxy);
  if (fm) await (await esc.setFeeManager(fm)).wait();

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "ProductEscrowV2", { proxy, impl });
  console.log(`ProductEscrowV2 proxy=${proxy} impl=${impl}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/product-escrow.test.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/product-escrow.js smart-contracts/test/deploy/product-escrow.test.js
git commit -m "feat(deploy): unified ProductEscrowV2 deploy module"
```

---

## Task 7: `deploy/service-contact.js`

**Files:**
- Create: `smart-contracts/deploy/service-contact.js`
- Test: `smart-contracts/test/deploy/service-contact.test.js`

**Interfaces:**
- Produces: `async function deploy(hre, { chain, feeManager, feeAmount } = {})` → `{ proxy, impl }`. `feeAmount` defaults to `0n` (admin sets real fees later via the seed/ops scripts); the deploy still calls `setTokenAcceptance` per token.

Read `scripts/deploy-mainnet-service-contact.js` to preserve `setTokenAcceptance` + `setFeeAmount` wiring.

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/service-contact.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/service-contact");

describe("deploy/service-contact", () => {
  it("deploys ServiceContactV2 and accepts configured tokens", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const sc = await hre.ethers.getContractAt("ServiceContactV2", proxy);
    expect(await sc.owner()).to.equal(chain.owner);
    expect(await sc.acceptedTokens(chain.tokens.USDC)).to.equal(true);
  });
});
```

Confirm `acceptedTokens` getter against `ServiceContactV2.sol`; adjust if different.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/service-contact.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `deploy/service-contact.js`**

```js
// smart-contracts/deploy/service-contact.js
const { resolveChain, writeLedger, readLedger } = require("./lib");

async function deploy(hre, { chain, feeManager, feeAmount = 0n } = {}) {
  const { ethers, upgrades, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());

  const Factory = await ethers.getContractFactory("ServiceContactV2");
  const sc = await upgrades.deployProxy(Factory, [owner], { kind: "uups" });
  await sc.waitForDeployment();
  const proxy = await sc.getAddress();

  for (const [sym, addr] of Object.entries(cfg.tokens)) {
    if (!addr) continue;
    await (await sc.setTokenAcceptance(addr, true)).wait();
    await (await sc.setTokenSymbol(addr, sym)).wait();
    if (feeAmount > 0n) await (await sc.setFeeAmount(addr, feeAmount)).wait();
  }
  const fm = feeManager || (chain ? null : readLedger(network.name).FeeManager?.proxy);
  if (fm) await (await sc.setFeeManager(fm)).wait();

  const impl = await upgrades.erc1967.getImplementationAddress(proxy);
  if (!chain) writeLedger(network.name, "ServiceContactV2", { proxy, impl });
  console.log(`ServiceContactV2 proxy=${proxy} impl=${impl}`);
  return { proxy, impl };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/service-contact.test.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/service-contact.js smart-contracts/test/deploy/service-contact.test.js
git commit -m "feat(deploy): unified ServiceContactV2 deploy module"
```

---

## Task 8: `deploy/mcgp-distributor.js` (with BSC fail-fast)

**Files:**
- Create: `smart-contracts/deploy/mcgp-distributor.js`
- Test: `smart-contracts/test/deploy/mcgp-distributor.test.js`

**Interfaces:**
- Produces: `async function deploy(hre, { chain } = {})` → `{ address }`. Throws via `requireAddr` when `tokens.MCGP` is null.

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/mcgp-distributor.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/mcgp-distributor");

describe("deploy/mcgp-distributor", () => {
  it("deploys against a configured MCGP token", async () => {
    const chain = await testChain();
    const { address } = await deploy(hre, { chain });
    const d = await hre.ethers.getContractAt("MCGPRewardDistributor", address);
    expect(await d.owner()).to.equal(chain.owner);
  });

  it("fails fast when MCGP is not configured (BSC)", async () => {
    const chain = await testChain({ tokens: { MCGP: null } });
    let err;
    try { await deploy(hre, { chain }); } catch (e) { err = e; }
    expect(err && err.message).to.match(/MCGP not configured/);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/mcgp-distributor.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `deploy/mcgp-distributor.js`**

```js
// smart-contracts/deploy/mcgp-distributor.js
const { resolveChain, requireAddr, writeLedger } = require("./lib");

async function deploy(hre, { chain } = {}) {
  const { ethers, network } = hre;
  const cfg = chain || resolveChain(hre);
  const [deployer] = await ethers.getSigners();
  const owner = cfg.owner || (await deployer.getAddress());
  const mcgp = requireAddr(cfg, "MCGP", cfg.tokens.MCGP);

  const Factory = await ethers.getContractFactory("MCGPRewardDistributor");
  const d = await Factory.deploy(mcgp, owner);
  await d.waitForDeployment();
  const address = await d.getAddress();
  if (!chain) writeLedger(network.name, "MCGPRewardDistributor", { address });
  console.log(`MCGPRewardDistributor address=${address} mcgp=${mcgp}`);
  return { address };
}

if (require.main === module) {
  deploy(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deploy };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/mcgp-distributor.test.js`
Expected: PASS (2 passing).

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/mcgp-distributor.js smart-contracts/test/deploy/mcgp-distributor.test.js
git commit -m "feat(deploy): unified MCGPRewardDistributor deploy module (BSC fail-fast)"
```

---

## Task 9: `deploy/upgrade.js`

**Files:**
- Create: `smart-contracts/deploy/upgrade.js`
- Test: `smart-contracts/test/deploy/upgrade.test.js`

**Interfaces:**
- Produces: `async function upgrade(hre, { contract, proxy } = {})` → `{ proxy, impl }`. `contract` is one of `"P2PEscrowV3" | "ProductEscrowV2" | "ServiceContactV2" | "FeeManager"`; `proxy` defaults to the ledger entry for the current network.

Read `scripts/upgrade-p2p-escrow-v3.js` to mirror the `upgradeProxy` + optional re-init `call` pattern (note V3's one-shot re-initializer noted in `P2PEscrowV3.sol`; the fresh-deploy path does NOT need it, only upgrades from V2 do — preserve that nuance if the old script used a `call` option).

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/upgrade.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deploy } = require("../../deploy/feemanager");
const { upgrade } = require("../../deploy/upgrade");

describe("deploy/upgrade", () => {
  it("upgrades an existing FeeManager proxy in place", async () => {
    const chain = await testChain();
    const { proxy } = await deploy(hre, { chain });
    const { proxy: same, impl } = await upgrade(hre, { contract: "FeeManager", proxy });
    expect(same).to.equal(proxy);
    expect(impl).to.match(/^0x[0-9a-fA-F]{40}$/);
  });
});
```

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/upgrade.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `deploy/upgrade.js`**

```js
// smart-contracts/deploy/upgrade.js
const { readLedger, writeLedger } = require("./lib");

const LEDGER_KEY = {
  P2PEscrowV3: "P2PEscrowV3",
  ProductEscrowV2: "ProductEscrowV2",
  ServiceContactV2: "ServiceContactV2",
  FeeManager: "FeeManager",
};

async function upgrade(hre, { contract, proxy } = {}) {
  const { ethers, upgrades, network } = hre;
  if (!LEDGER_KEY[contract]) throw new Error(`deploy/upgrade: unknown contract '${contract}'`);
  const target = proxy || readLedger(network.name)[LEDGER_KEY[contract]]?.proxy;
  if (!target) throw new Error(`deploy/upgrade: no proxy for ${contract} on ${network.name}`);

  const Factory = await ethers.getContractFactory(contract);
  const upgraded = await upgrades.upgradeProxy(target, Factory, { kind: "uups" });
  await upgraded.waitForDeployment();
  const addr = await upgraded.getAddress();
  const impl = await upgrades.erc1967.getImplementationAddress(addr);
  if (!proxy) writeLedger(network.name, LEDGER_KEY[contract], { proxy: addr, impl });
  console.log(`${contract} upgraded proxy=${addr} impl=${impl}`);
  return { proxy: addr, impl };
}

if (require.main === module) {
  const contract = process.env.UPGRADE_CONTRACT;
  upgrade(require("hardhat"), { contract }).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { upgrade };
```

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/upgrade.test.js`
Expected: PASS.

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/upgrade.js smart-contracts/test/deploy/upgrade.test.js
git commit -m "feat(deploy): config-driven proxy upgrade module"
```

---

## Task 10: `deploy/all.js` orchestrator

**Files:**
- Create: `smart-contracts/deploy/all.js`
- Test: `smart-contracts/test/deploy/all.test.js`

**Interfaces:**
- Produces: `async function deployAll(hre, { chain } = {})` → `{ FeeManager, P2PEscrowV3, ProductEscrowV2, ServiceContactV2, MCGPRewardDistributor? }`. Deploys FeeManager first, passes its proxy to each escrow as `feeManager`, and only deploys the distributor when `tokens.MCGP` is set.

- [ ] **Step 1: Write the failing test**

```js
// smart-contracts/test/deploy/all.test.js
const { expect } = require("chai");
const hre = require("hardhat");
const { testChain } = require("./helpers");
const { deployAll } = require("../../deploy/all");

describe("deploy/all", () => {
  it("deploys the full stack and wires escrows to the FeeManager", async () => {
    const chain = await testChain();
    const out = await deployAll(hre, { chain });
    expect(out.FeeManager.proxy).to.match(/^0x/);
    const esc = await hre.ethers.getContractAt("P2PEscrowV3", out.P2PEscrowV3.proxy);
    expect(await esc.feeManager()).to.equal(out.FeeManager.proxy);
    expect(out.MCGPRewardDistributor.address).to.match(/^0x/);
  });

  it("omits the distributor when MCGP is null (BSC)", async () => {
    const chain = await testChain({ tokens: { MCGP: null } });
    const out = await deployAll(hre, { chain });
    expect(out.MCGPRewardDistributor).to.equal(undefined);
  });
});
```

Confirm the `feeManager()` getter name on `P2PEscrowV3.sol`; adjust if different.

- [ ] **Step 2: Run test to verify it fails**

Run: `cd smart-contracts && npx hardhat test test/deploy/all.test.js`
Expected: FAIL — module not found.

- [ ] **Step 3: Write `deploy/all.js`**

```js
// smart-contracts/deploy/all.js
const { resolveChain } = require("./lib");
const feemanager = require("./feemanager");
const p2p = require("./p2p-escrow");
const product = require("./product-escrow");
const service = require("./service-contact");
const distributor = require("./mcgp-distributor");

async function deployAll(hre, { chain } = {}) {
  const cfg = chain || resolveChain(hre);
  const out = {};
  out.FeeManager = await feemanager.deploy(hre, { chain: cfg });
  const fm = out.FeeManager.proxy;
  out.P2PEscrowV3 = await p2p.deploy(hre, { chain: cfg, feeManager: fm });
  out.ProductEscrowV2 = await product.deploy(hre, { chain: cfg, feeManager: fm });
  out.ServiceContactV2 = await service.deploy(hre, { chain: cfg, feeManager: fm });
  if (cfg.tokens.MCGP) out.MCGPRewardDistributor = await distributor.deploy(hre, { chain: cfg });
  else console.log("MCGP not configured for this network — skipping MCGPRewardDistributor");
  return out;
}

if (require.main === module) {
  deployAll(require("hardhat")).catch((e) => { console.error(e); process.exit(1); });
}

module.exports = { deployAll };
```

Note: when invoked via CLI (`chain` undefined) each sub-module resolves the chain and writes the ledger itself; passing `{ chain: cfg }` here is for the in-process test path. For the real CLI path, change the calls to omit `chain` so the ledger is written — implement by branching on whether `chain` was provided (pass `chain` only when supplied). Concretely: compute `const passChain = chain ? { chain: cfg } : {}` and spread it, plus `feeManager: fm`, into each call.

Apply that refinement so the CLI path persists the ledger; re-run the test to confirm it still passes.

- [ ] **Step 4: Run test to verify it passes**

Run: `cd smart-contracts && npx hardhat test test/deploy/all.test.js`
Expected: PASS (2 passing).

- [ ] **Step 5: Commit**

```bash
git add smart-contracts/deploy/all.js smart-contracts/test/deploy/all.test.js
git commit -m "feat(deploy): full-stack orchestrator in dependency order"
```

---

## Task 11: Refactor seed scripts to read `chains.js`

**Files:**
- Modify: `smart-contracts/scripts/seed-feemanager-rates.js`
- Modify: `smart-contracts/scripts/seed-feemanager-pm-rates.js`

**Interfaces:**
- Consumes: `resolveChain`, `readLedger` (lib).

- [ ] **Step 1: Read both scripts**

Identify every hardcoded token/FeeManager address. The FeeManager address should come from `readLedger(network.name).FeeManager.proxy` (fallback to `process.env.FEE_MANAGER_ADDRESS`); token addresses from `resolveChain(hre).tokens`. Keep the rate values exactly as they are.

- [ ] **Step 2: Apply the refactor**

Replace the hardcoded address constants with:

```js
const { resolveChain, readLedger } = require("../deploy/lib");
const hre = require("hardhat");
const cfg = resolveChain(hre);
const feeManagerAddr = process.env.FEE_MANAGER_ADDRESS || readLedger(hre.network.name).FeeManager?.proxy;
if (!feeManagerAddr) throw new Error("seed: FeeManager address not found (deploy it or set FEE_MANAGER_ADDRESS)");
// token addresses: cfg.tokens.USDT / cfg.tokens.USDC / cfg.tokens.MCGP
```

Leave the rate-table values untouched.

- [ ] **Step 3: Verify they still parse/compile**

Run: `cd smart-contracts && node -c scripts/seed-feemanager-rates.js && node -c scripts/seed-feemanager-pm-rates.js`
Expected: no syntax errors (exit 0).

- [ ] **Step 4: Commit**

```bash
git add smart-contracts/scripts/seed-feemanager-rates.js smart-contracts/scripts/seed-feemanager-pm-rates.js
git commit -m "refactor(deploy): seed scripts source addresses from chains.js"
```

---

## Task 12: Full dry-run, delete superseded scripts, update docs

**Files:**
- Delete: the superseded deploy/wire/upgrade scripts (list below).
- Modify: `smart-contracts/.env.example`, `smart-contracts/README.md` (or create a `deploy/README.md`), `smart-contracts/.gitignore`.
- Create: `smart-contracts/deployments/.gitkeep`.

- [ ] **Step 1: Run the whole deploy test suite + compile**

Run: `cd smart-contracts && npx hardhat compile && npx hardhat test test/deploy/`
Expected: compile clean; all deploy tests PASS. Do not proceed to deletion until green.

- [ ] **Step 2: Reconcile the delete-list against the live tree**

Run: `cd smart-contracts && ls scripts/ | grep -E '^(deploy|wire|upgrade)'`
Cross-check every printed deploy/wire/upgrade filename against the spec's replacement map. For each, confirm its behavior now lives in a `deploy/*.js` module (or is an intentional keep). If a script appears that the spec did not anticipate, STOP and surface it rather than deleting blindly.

- [ ] **Step 3: Delete the superseded scripts**

```bash
cd smart-contracts && git rm \
  scripts/deploy-feemanager.js \
  scripts/deploy-mainnet-escrow.js \
  scripts/deploy-mainnet-service-contact.js \
  scripts/deploy-mcgp-distributor.js \
  scripts/deploy-p2p-escrow.js \
  scripts/deploy-p2p-escrow-v2.js \
  scripts/deploy-bsc-testnet-p2p-bundle.js \
  scripts/deploy-bsc-testnet-p2p-v2-bundle.js \
  scripts/deploy-fresh-escrows-v2-testnet.js \
  scripts/deploy-testnet.js \
  scripts/deploy-testnet-p2p-bundle.js \
  scripts/deploy-testnet-upgradeable.js \
  scripts/wire-bsc-p2p-escrow.js \
  scripts/wire-p2p-escrow-feemanager.js \
  scripts/upgrade-p2p-escrow-v3.js \
  scripts/upgrade-product-escrow-v2.js \
  scripts/upgrade-service-contact-v2.js
```

If any path above does not exist (filename drift), drop it from the command — do not force. Keep `deploy-bsc-test-tokens.js` and all `check-*`/`smoke-*`/ops scripts.

- [ ] **Step 4: Add the ledger dir + gitignore, update docs**

Add to `smart-contracts/.gitignore`:

```
deployments/*.json
```

Create `smart-contracts/deployments/.gitkeep` (empty). Then write a short `smart-contracts/deploy/README.md`:

```markdown
# Deploy tooling

Per-chain config lives in `deploy/chains.js` (addresses only; keys in `.env`).
Deploy any contract to any network:

    npx hardhat run deploy/feemanager.js --network bscTestnet
    npx hardhat run deploy/all.js --network sonicMainnet      # full stack, in order

Networks: sonicMainnet, sonicTestnet, bscMainnet, bscTestnet.
MCGPRewardDistributor requires an MCGP token; it is skipped/fails-fast on BSC
until `chains.js` has a BSC MCGP address. Upgrades:

    UPGRADE_CONTRACT=FeeManager npx hardhat run deploy/upgrade.js --network sonicMainnet

Deployed addresses are recorded in `deployments/<network>.json` (git-ignored).
```

Update `.env.example`: remove now-dead per-script address vars that moved into `chains.js` defaults, and document the override vars (`MAINNET_*`, `TESTNET_*`, `BSC_MAINNET_*`, `BSC_TESTNET_*`, `OWNER_ADDRESS`, `ADMIN_ADDRESSES`, `SYSTEM_WALLET_ADDRESS`, `FEE_MANAGER_ADDRESS`). Keep `DEPLOYER_PRIVATE_KEY`.

- [ ] **Step 5: Re-run compile + tests to confirm nothing referenced a deleted script**

Run: `cd smart-contracts && npx hardhat compile && npx hardhat test`
Expected: full suite PASS (contracts + deploy). If a kept script `require`d a deleted one, fix the reference.

- [ ] **Step 6: Commit**

```bash
git add -A
git commit -m "refactor(deploy): remove legacy scripts; docs + ledger for unified tooling"
```

---

## Self-Review Notes

- **Spec coverage:** chains.js (Task 1) ✓; lib.js (Task 2) ✓; per-contract modules incl. MCGP fail-fast (Tasks 4-8) ✓; upgrades (Task 9) ✓; orchestration + idempotency-by-ledger (Task 10) ✓; seed refactor (Task 11) ✓; deletions + dry-run + config-completeness + docs (Tasks 1, 12) ✓.
- **Idempotency** is realized via the ledger (`writeLedger`/`readLedger`) and the CLI-path branch in each module; the in-process tests exercise the `chain`-override path.
- **Getter-name caveat:** assertions reference public getters (`systemWallet`, `acceptedTokens`, `mcgpToken`, `feeManager`) that must be confirmed against each contract; every such task says to adjust if the real getter differs. This is the one place the implementer must verify against source.
- **No live transactions:** all verification runs on the in-process Hardhat network with mock tokens.
```
