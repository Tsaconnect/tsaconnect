const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");

// Stateful invariant fuzzing for P2PEscrow (same approach as OrderEscrow.invariants.test.js).
// After EVERY random step:
//   P1  owed[token] == Σ available + Σ locked + Σ claimable, and balanceOf(escrow) >= owed
//   P2  locked[seller] == Σ amount of that seller's open trades (Pending / Paid / Disputed)
//   P3  voucherFilled[h] <= maxAmount and == Σ amount of its trades not returned to the pool
//   P4  conservation: the sum over every holder equals what was minted
//   P5  strangers (no role, not a party, not a releaser) never gain tokens
//   P6  a seller can always withdraw their whole available balance, paused or not
//   P7  liveness: every open trade can be ended without an admin
// Reproduce a failure with SEED=<n>.

const PLATFORM = ethers.encodeBytes32String("p2p_platform");
const GAS = ethers.encodeBytes32String("p2p_gas");
const DAY = 24 * 60 * 60;
const S = { None: 0, Pending: 1, Paid: 2, Released: 3, Cancelled: 4, Disputed: 5, Resolved: 6 };
const OPEN = new Set([S.Pending, S.Paid, S.Disputed]);
const P2P = "contracts/core/P2PEscrow.sol:P2PEscrow";

function prng(seed) {
  let x = seed >>> 0 || 1;
  return () => {
    x ^= x << 13; x >>>= 0;
    x ^= x >>> 17;
    x ^= x << 5; x >>>= 0;
    return x / 0x100000000;
  };
}

const RUNS = process.env.SEED ? 1 : Number(process.env.INVARIANT_RUNS || 10);
const STEPS = Number(process.env.INVARIANT_STEPS || 60);

describe("P2PEscrow invariants (stateful fuzz)", function () {
  this.timeout(15 * 60 * 1000);

  for (let run = 0; run < RUNS; run++) {
    const seed = process.env.SEED ? Number(process.env.SEED) : 2000 + run;

    it(`seed ${seed}: ${STEPS} random steps keep every invariant`, async () => {
      const rand = prng(seed);
      const pick = (arr) => arr[Math.floor(rand() * arr.length)];
      const between = (lo, hi) => lo + Math.floor(rand() * (hi - lo + 1));

      const signers = await ethers.getSigners();
      const [admin, resolver, pauser, stranger, feeA, feeB, claimDest, releaser] = signers;
      const sellers = signers.slice(8, 11);
      const buyers = signers.slice(11, 14);
      const traders = [...sellers, ...buyers];

      const fc = await upgrades.deployProxy(await ethers.getContractFactory("FeeConfig"), [admin.address, feeA.address, { features: [], tokens: [], rates: [], limitTokens: [], limits: [] }], { kind: "uups" });
      const fwd = await (await ethers.getContractFactory("TSAForwarder")).deploy("TSAForwarder");
      const escrow = await upgrades.deployProxy(await ethers.getContractFactory(P2P), [admin.address, [], []], {
        kind: "uups",
        constructorArgs: [await fwd.getAddress(), await fc.getAddress()],
      });
      const token = await (await ethers.getContractFactory("BlacklistERC20")).deploy("USDT", "USDT", 6);
      const T = await token.getAddress();
      const E = await escrow.getAddress();
      await fc.setRate(PLATFORM, T, { bps: 100, flatFee: 0, active: true });
      await fc.setRate(GAS, T, { bps: 50, flatFee: 0, active: true });
      await fc.setLimits(T, { maxPerOrder: 2_000_000_000n, maxOwed: 20_000_000_000n });
      await escrow.grantRole(await escrow.RESOLVER_ROLE(), resolver.address);
      await escrow.grantRole(await escrow.PAUSER_ROLE(), pauser.address);

      let minted = 0n;
      for (const t of traders) {
        await token.mint(t.address, 5_000_000_000n);
        minted += 5_000_000_000n;
        await token.connect(t).approve(E, ethers.MaxUint256);
      }
      const holders = [E, admin, resolver, pauser, stranger, feeA, feeB, claimDest, releaser, ...traders].map((h) => h.address ?? h);
      const payees = [...traders, feeA, feeB];
      const strangerStart = await token.balanceOf(stranger.address);

      const { chainId } = await ethers.provider.getNetwork();
      const domain = { name: "TSAConnectP2PEscrow", version: "3", chainId, verifyingContract: E };
      const types = {
        AdVoucher: [
          { name: "seller", type: "address" }, { name: "token", type: "address" }, { name: "maxAmount", type: "uint256" },
          { name: "minPerTrade", type: "uint256" }, { name: "maxPerTrade", type: "uint256" }, { name: "deadline", type: "uint256" },
          { name: "nonce", type: "bytes32" }, { name: "taker", type: "address" },
        ],
      };

      const vouchers = []; // { v, sig, hash }
      const trades = new Map(); // key -> { seller, buyer, amount, voucherHash, funding }
      let nonce = 0;
      let paused = false;

      const tryTx = async (p) => { try { await p; return true; } catch { return false; } };
      const sync = async (k) => { trades.get(k).status = Number((await escrow.getTrade(k)).status); };
      const openKeys = () => [...trades.keys()].filter((k) => OPEN.has(trades.get(k).status));

      async function check(label) {
        let pools = 0n;
        for (const s of traders) {
          const [a, l] = await escrow.balanceOf(s.address, T);
          pools += a + l;
          let lockedExpected = 0n;
          for (const k of openKeys()) if (trades.get(k).seller === s) lockedExpected += trades.get(k).amount;
          expect(l, `P2 ${label}`).to.equal(lockedExpected);
        }
        for (const p of payees) pools += await escrow.claimable(T, p.address);
        const owed = await escrow.owed(T);
        expect(owed, `P1 owed ${label}`).to.equal(pools);
        expect((await token.balanceOf(E)) >= owed, `P1 balance ${label}`).to.equal(true);
        for (const vc of vouchers) {
          const filled = await escrow.voucherFilled(vc.hash);
          let expected = 0n;
          for (const t of trades.values()) {
            if (t.voucherHash !== vc.hash) continue;
            if (t.status === S.Cancelled || (t.status === S.Resolved && !t.toBuyer)) continue;
            expected += t.amount;
          }
          expect(filled, `P3 ${label}`).to.equal(expected);
          expect(filled <= vc.v.maxAmount, `P3 cap ${label}`).to.equal(true);
        }
        let total = 0n;
        for (const h of holders) total += await token.balanceOf(h);
        expect(total, `P4 ${label}`).to.equal(minted);
        expect(await token.balanceOf(stranger.address), `P5 ${label}`).to.equal(strangerStart);
      }

      const actions = {
        async deposit() {
          const s = pick(sellers);
          const ok = await tryTx(escrow.connect(s).deposit(T, BigInt(between(1, 500)) * 1_000_000n));
          if (paused) expect(ok, "deposit while paused").to.equal(false);
        },
        async withdraw() {
          const s = pick(sellers);
          const [a] = await escrow.balanceOf(s.address, T);
          if (a === 0n || (await token.blacklisted(s.address))) return;
          const amt = rand() < 0.5 ? a : a / 2n || a;
          expect(await tryTx(escrow.connect(s).withdraw(T, amt)), "P6 withdraw").to.equal(true);
        },
        async newVoucher() {
          const s = pick(sellers);
          const max = BigInt(between(50, 800)) * 1_000_000n;
          const v = { seller: s.address, token: T, maxAmount: max, minPerTrade: 0n, maxPerTrade: 0n,
            deadline: BigInt((await time.latest()) + 30 * DAY), nonce: ethers.id(`v-${seed}-${nonce++}`), taker: ethers.ZeroAddress };
          const sig = await s.signTypedData(domain, types, v);
          vouchers.push({ v, sig, hash: await escrow.hashVoucher(v) });
        },
        async open() {
          const vc = pick(vouchers); if (!vc) return;
          const b = pick(buyers);
          const tid = ethers.id(`t-${seed}-${nonce++}`);
          const amount = BigInt(between(1, 300)) * 1_000_000n;
          if (await tryTx(escrow.connect(b).openTrade(tid, vc.v, vc.sig, amount))) {
            const k = await escrow.tradeKey(b.address, tid);
            trades.set(k, { seller: sellers.find((s) => s.address === vc.v.seller), buyer: b, amount, voucherHash: vc.hash, status: S.Pending });
          }
        },
        async openWithDeposit() {
          const u = pick(traders);
          const m = pick(traders.filter((t) => t !== u));
          const tid = ethers.id(`d-${seed}-${nonce++}`);
          const amount = BigInt(between(1, 300)) * 1_000_000n;
          if (await tryTx(escrow.connect(u).openTradeWithDeposit(tid, T, amount, m.address))) {
            const k = await escrow.tradeKey(u.address, tid);
            trades.set(k, { seller: u, buyer: m, amount, voucherHash: ethers.ZeroHash, status: S.Pending });
          }
        },
        async markPaid() { const k = pick(openKeys()); if (!k) return; await tryTx(escrow.connect(trades.get(k).buyer).markPaid(k)); await sync(k); },
        async release() {
          const k = pick(openKeys()); if (!k) return;
          const t = trades.get(k);
          const by = rand() < 0.3 ? releaser : t.seller;
          if (await tryTx(escrow.connect(by).release(k))) t.toBuyer = true;
          await sync(k);
        },
        async strangerRelease() {
          const k = pick(openKeys()); if (!k) return;
          expect(await tryTx(escrow.connect(stranger).release(k)), "stranger release").to.equal(false);
          expect(await tryTx(escrow.connect(admin).release(k)), "admin release").to.equal(false);
        },
        async setReleaser() { const s = pick(sellers); await escrow.connect(s).setReleaser(rand() < 0.5 ? releaser.address : ethers.ZeroAddress); },
        async setAuto() { const s = pick(traders); await escrow.connect(s).setAutoReleaseWindow(pick([0, 1800, 3600, DAY])); },
        async finalize() { const k = pick(openKeys()); if (!k) return; if (await tryTx(escrow.connect(stranger).finalizeRelease(k))) trades.get(k).toBuyer = true; await sync(k); },
        async cancel() { const k = pick(openKeys()); if (!k) return; await tryTx(escrow.connect(stranger).cancelByTimeout(k)); await sync(k); },
        async dispute() { const k = pick(openKeys()); if (!k) return; const t = trades.get(k); await tryTx(escrow.connect(rand() < 0.5 ? t.buyer : t.seller).raiseDispute(k)); await sync(k); },
        async resolve() {
          const k = pick(openKeys()); if (!k) return;
          const toBuyer = rand() < 0.5;
          if (await tryTx(escrow.connect(resolver).adminResolve(k, toBuyer))) trades.get(k).toBuyer = toBuyer;
          await sync(k);
        },
        async backstop() { const k = pick(openKeys()); if (!k) return; await tryTx(escrow.connect(stranger).claimDisputeBackstop(k)); await sync(k); },
        async wait() { await time.increase(pick([60, 1800, 3600, DAY, 15 * DAY, 61 * DAY])); },
        async pauseToggle() {
          if (paused) { await escrow.connect(admin).unpause(); paused = false; } else { await escrow.connect(pauser).pause(); paused = true; }
        },
        async blacklist() { const w = pick(payees); await token.setBlacklisted(w.address, !(await token.blacklisted(w.address))); },
        async claim() {
          const w = pick(payees);
          if ((await escrow.claimable(T, w.address)) === 0n) return;
          await escrow.connect(w).claim(T, (await token.blacklisted(w.address)) ? claimDest.address : w.address);
        },
        async stray() { const amt = BigInt(between(1, 50)) * 1_000_000n; await token.mint(E, amt); minted += amt; },
        async rescue() { await tryTx(escrow.connect(admin).rescue(T)); },
        async reconfigure() {
          await fc.setRate(PLATFORM, T, { bps: between(0, 300), flatFee: 0, active: true });
          await fc.setRate(GAS, T, { bps: between(0, 200), flatFee: 0, active: true });
          await fc.setFeeRecipient(pick([feeA, feeB]).address);
        },
      };

      const weighted = [
        ["deposit", 4], ["withdraw", 2], ["newVoucher", 3], ["open", 6], ["openWithDeposit", 2], ["markPaid", 4],
        ["release", 3], ["strangerRelease", 1], ["setReleaser", 1], ["setAuto", 1], ["finalize", 2], ["cancel", 2],
        ["dispute", 2], ["resolve", 2], ["backstop", 1], ["wait", 3], ["pauseToggle", 1], ["blacklist", 1],
        ["claim", 1], ["stray", 1], ["rescue", 1], ["reconfigure", 1],
      ];
      const bag = weighted.flatMap(([n, w]) => Array(w).fill(n));

      for (let step = 0; step < STEPS; step++) {
        const name = pick(bag);
        await actions[name]();
        await check(`seed ${seed} step ${step} (${name})`);
      }

      if (process.env.FUZZ_STATS) {
        const tally = {};
        for (const t of trades.values()) tally[t.status] = (tally[t.status] || 0) + 1;
        console.log(`      seed ${seed} trades=${trades.size} vouchers=${vouchers.length} states=${JSON.stringify(tally)}`);
      }

      // P7: end every open trade with no admin: unpaid ones time out; paid ones go through a
      // party's dispute and the backstop.
      if (!paused) { await escrow.connect(pauser).pause(); paused = true; }
      await time.increase(DAY);
      for (const k of openKeys()) {
        const t = trades.get(k);
        if (t.status === S.Pending) await escrow.connect(stranger).cancelByTimeout(k);
        else if (t.status === S.Paid && !(await tryTx(escrow.connect(t.buyer).raiseDispute(k)))) {
          await escrow.connect(stranger).finalizeRelease(k); // auto-release already due
        }
        await sync(k);
      }
      await time.increase(61 * DAY);
      for (const k of openKeys()) {
        const t = trades.get(k);
        // A paid trade whose auto-release came due cannot be disputed; finalize it instead.
        if (t.status === S.Paid) await escrow.connect(stranger).finalizeRelease(k);
        else await escrow.connect(stranger).claimDisputeBackstop(k);
        await sync(k);
      }
      expect(openKeys().length, "P7 liveness").to.equal(0);
      await check(`seed ${seed} drained`);
    });
  }
});
