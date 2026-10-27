const { expect } = require("chai");
const { ethers, upgrades } = require("hardhat");
const { time } = require("@nomicfoundation/hardhat-network-helpers");

// Stateful invariant fuzzing for OrderEscrow, written for Hardhat (Foundry is not installed in
// this repo yet). Each run plays a seeded random sequence of actions by several buyers, sellers
// and admins, and after EVERY step checks the invariants in the redesign spec §9:
//   I1  token.balanceOf(escrow) >= owed[token]
//   I2  owed[token] == Σ (product + shipping) of orders that are not terminal + Σ claimable payouts
//       (the token can blacklist addresses, like USDC/USDT, so payouts are sometimes deferred)
//   I3  conservation: no token is created or lost — the sum over every holder equals what was minted
//   I4  only buyer, seller or the fee recipient ever receives tokens (strangers never gain)
//   I5  terminal states are absorbing: every action on a terminal order reverts
//   I6  the merchant fee never exceeds 3% of the product price, and never changes after creation
//   I7  liveness: every open order can be settled without an admin, even while paused
// Reproduce a failure with SEED=<n> npx hardhat test test/core/OrderEscrow.invariants.test.js

const FEATURE = ethers.encodeBytes32String("order");
const DAY = 24 * 60 * 60;
const S = { None: 0, Funded: 1, Delivered: 2, Disputed: 3, Released: 4, Refunded: 5, Resolved: 6 };
const TERMINAL = new Set([S.Released, S.Refunded, S.Resolved]);

function prng(seed) {
  let x = seed >>> 0 || 1;
  return () => {
    x ^= x << 13; x >>>= 0;
    x ^= x >>> 17;
    x ^= x << 5; x >>>= 0;
    return x / 0x100000000;
  };
}

const RUNS = process.env.SEED ? 1 : Number(process.env.INVARIANT_RUNS || 12);
const STEPS = Number(process.env.INVARIANT_STEPS || 60);

describe("OrderEscrow invariants (stateful fuzz)", function () {
  this.timeout(10 * 60 * 1000);

  for (let run = 0; run < RUNS; run++) {
    const seed = process.env.SEED ? Number(process.env.SEED) : 1000 + run;

    it(`seed ${seed}: ${STEPS} random steps keep every invariant`, async () => {
      const rand = prng(seed);
      const pick = (arr) => arr[Math.floor(rand() * arr.length)];
      const between = (lo, hi) => lo + Math.floor(rand() * (hi - lo + 1));

      const signers = await ethers.getSigners();
      const [admin, resolver, pauser, stranger, feeA, feeB, claimDest] = signers;
      const buyers = signers.slice(7, 10);
      const sellers = signers.slice(10, 13);

      const fc = await upgrades.deployProxy(await ethers.getContractFactory("FeeConfig"), [admin.address, feeA.address, { features: [], tokens: [], rates: [], limitTokens: [], limits: [] }], { kind: "uups" });
      const fwd = await (await ethers.getContractFactory("TSAForwarder")).deploy("TSAForwarder");
      const escrow = await upgrades.deployProxy(await ethers.getContractFactory("OrderEscrow"), [admin.address, [], []], {
        kind: "uups",
        constructorArgs: [await fwd.getAddress(), await fc.getAddress()],
      });
      const token = await (await ethers.getContractFactory("BlacklistERC20")).deploy("USDC", "USDC", 6);
      const T = await token.getAddress();
      const E = await escrow.getAddress();
      await fc.setRate(FEATURE, T, { bps: 200, flatFee: 100000n, active: true });
      await fc.setLimits(T, { maxPerOrder: 5_000_000_000n, maxOwed: 50_000_000_000n });
      await escrow.grantRole(await escrow.RESOLVER_ROLE(), resolver.address);
      await escrow.grantRole(await escrow.PAUSER_ROLE(), pauser.address);

      let minted = 0n;
      for (const b of buyers) {
        await token.mint(b.address, 1_000_000_000_000n);
        minted += 1_000_000_000_000n;
        await token.connect(b).approve(E, ethers.MaxUint256);
      }

      const holders = [E, admin.address, resolver.address, pauser.address, stranger.address, feeA.address, feeB.address, claimDest.address,
        ...buyers.map((b) => b.address), ...sellers.map((s) => s.address)];
      const strangerStart = await token.balanceOf(stranger.address);

      // Ghost model.
      const orders = new Map(); // key -> { buyer, seller, gross, product, fee }
      const payees = [...buyers, ...sellers, feeA, feeB];
      let nextId = 0;
      let paused = false;

      const ids = () => [...orders.keys()];
      const open = () => ids().filter((k) => !TERMINAL.has(orders.get(k).state));

      async function sync(k) {
        const o = await escrow.getOrder(k);
        orders.get(k).state = Number(o.state);
        return o;
      }

      async function tryTx(p) {
        try { await p; return true; } catch { return false; }
      }

      async function checkInvariants(stepLabel) {
        const owedOnChain = await escrow.owed(T);
        const balance = await token.balanceOf(E);
        expect(balance >= owedOnChain, `I1 ${stepLabel}`).to.equal(true);
        let expectedOwed = 0n;
        for (const k of open()) expectedOwed += orders.get(k).gross;
        for (const p of payees) expectedOwed += await escrow.claimable(T, p.address);
        expect(owedOnChain, `I2 ${stepLabel}`).to.equal(expectedOwed);
        let total = 0n;
        for (const h of holders) total += await token.balanceOf(h);
        expect(total, `I3 ${stepLabel}`).to.equal(minted);
        expect(await token.balanceOf(stranger.address), `I4 ${stepLabel}`).to.equal(strangerStart);
      }

      const actions = {
        async create() {
          if (paused) {
            const k = ethers.id(`p-${seed}-${nextId++}`);
            const ok = await tryTx(escrow.connect(pick(buyers)).createOrder(k, sellers[0].address, T, 1_000_000n, 0, 14, ethers.MaxUint256));
            expect(ok, "create must fail while paused").to.equal(false);
            return;
          }
          const oid = ethers.id(`o-${seed}-${nextId++}`);
          const buyer = pick(buyers);
          const k = await escrow.orderKey(buyer.address, oid);
          const seller = pick(sellers);
          const product = BigInt(between(1, 2_000)) * 1_000_000n;
          const shipping = BigInt(between(0, 50)) * 1_000_000n;
          const days = between(1, 30);
          const ok = await tryTx(escrow.connect(buyer).createOrder(oid, seller.address, T, product, shipping, days, ethers.MaxUint256));
          if (!ok) return; // limits may refuse it; the invariants still hold
          const o = await escrow.getOrder(k);
          orders.set(k, { buyer, seller, gross: product + shipping, product, fee: o.merchantFee, state: Number(o.state) });
          expect(o.merchantFee * 10_000n <= product * 300n, "I6 fee cap").to.equal(true);
        },
        async deliver() {
          const k = pick(open()); if (!k) return;
          const m = orders.get(k);
          await tryTx(escrow.connect(m.seller).markDelivered(k));
          await sync(k);
        },
        async confirm() {
          const k = pick(open()); if (!k) return;
          await tryTx(escrow.connect(orders.get(k).buyer).confirmReceipt(k));
          await sync(k);
        },
        async cancel() {
          const k = pick(open()); if (!k) return;
          await tryTx(escrow.connect(orders.get(k).seller).cancel(k));
          await sync(k);
        },
        async dispute() {
          const k = pick(open()); if (!k) return;
          await tryTx(escrow.connect(orders.get(k).buyer).dispute(k));
          await sync(k);
        },
        async resolve() {
          const k = pick(open()); if (!k) return;
          const m = orders.get(k);
          const toBuyer = (m.gross * BigInt(between(0, 100))) / 100n;
          await tryTx(escrow.connect(resolver).resolve(k, toBuyer));
          await sync(k);
        },
        async timeout() {
          const k = pick(open()); if (!k) return;
          await tryTx(escrow.connect(stranger).claimTimeout(k));
          await sync(k);
        },
        async wait() {
          await time.increase(pick([60, 3600, DAY, 3 * DAY, 8 * DAY, 15 * DAY]));
        },
        async pauseToggle() {
          if (paused) { await escrow.connect(admin).unpause(); paused = false; }
          else { await escrow.connect(pauser).pause(); paused = true; }
        },
        async stray() {
          const amt = BigInt(between(1, 100)) * 1_000_000n;
          await token.mint(E, amt);
          minted += amt;
        },
        async rescue() {
          await tryTx(escrow.connect(admin).rescue(T));
        },
        async reconfigure() {
          await fc.setRate(FEATURE, T, { bps: between(0, 300), flatFee: BigInt(between(0, 200_000)), active: true });
          await fc.setFeeRecipient(pick([feeA, feeB]).address);
        },
        async blacklist() {
          const who = pick(payees);
          await token.setBlacklisted(who.address, !(await token.blacklisted(who.address)));
        },
        async claim() {
          const who = pick(payees);
          if ((await escrow.claimable(T, who.address)) === 0n) return;
          const to = (await token.blacklisted(who.address)) ? claimDest.address : who.address;
          await escrow.connect(who).claim(T, to);
        },
        async terminalPoke() {
          const done = ids().filter((k) => TERMINAL.has(orders.get(k).state));
          const k = pick(done); if (!k) return;
          const m = orders.get(k);
          const attempts = [
            escrow.connect(m.seller).markDelivered(k),
            escrow.connect(m.buyer).confirmReceipt(k),
            escrow.connect(m.seller).cancel(k),
            escrow.connect(m.buyer).dispute(k),
            escrow.connect(resolver).resolve(k, 0),
            escrow.connect(stranger).claimTimeout(k),
          ];
          for (const a of attempts) expect(await tryTx(a), "I5 terminal is absorbing").to.equal(false);
        },
      };

      const weighted = [
        ["create", 6], ["deliver", 4], ["confirm", 3], ["cancel", 1], ["dispute", 2], ["resolve", 2],
        ["timeout", 3], ["wait", 3], ["pauseToggle", 1], ["stray", 1], ["rescue", 1], ["reconfigure", 1], ["terminalPoke", 2],
        ["blacklist", 2], ["claim", 2],
      ];
      const bag = weighted.flatMap(([n, w]) => Array(w).fill(n));

      for (let step = 0; step < STEPS; step++) {
        const name = pick(bag);
        await actions[name]();
        // I6: the snapshot never moves after creation.
        for (const k of open()) expect((await escrow.getOrder(k)).merchantFee, "I6 snapshot").to.equal(orders.get(k).fee);
        await checkInvariants(`seed ${seed} step ${step} (${name})`);
      }

      if (process.env.FUZZ_STATS) {
        const tally = {};
        for (const m of orders.values()) tally[m.state] = (tally[m.state] || 0) + 1;
        console.log(`      seed ${seed} orders=${orders.size} states=${JSON.stringify(tally)}`);
      }

      // I7: everything still open settles with no admin, even while paused — and even with every
      // payee still blacklisted, because payouts defer instead of reverting.
      if (!paused) { await escrow.connect(pauser).pause(); paused = true; }
      await time.increase(31 * DAY);
      for (const k of open()) {
        await escrow.connect(stranger).claimTimeout(k);
        await sync(k);
        if (orders.get(k).state === S.Disputed) {
          await time.increase(15 * DAY);
          await escrow.connect(stranger).claimTimeout(k);
          await sync(k);
        }
      }
      expect(open().length, "I7 liveness").to.equal(0);
      // Every deferred payout can be collected, to a fresh address if the payee is still blacklisted.
      for (const p of payees) {
        if ((await escrow.claimable(T, p.address)) === 0n) continue;
        const to = (await token.blacklisted(p.address)) ? claimDest.address : p.address;
        await escrow.connect(p).claim(T, to);
      }
      expect(await escrow.owed(T)).to.equal(0n);
      await checkInvariants(`seed ${seed} drained`);
    });
  }
});
