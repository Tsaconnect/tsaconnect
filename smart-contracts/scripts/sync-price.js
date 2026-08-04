// Mirror the LIVE V1 premium prices onto the new swap. The new deploy was
// commissioned with the old deploy-time defaults ($0.02/$0.018) rather than
// the prices actually in production, so repointing the backend without this
// would misprice MCGP 5x. Dry-run by default.
const { ethers } = require("hardhat");
const V1="0xF27Fe8091c31FAa0101B8Ef86421035bbAda8E52";
const NEW=process.env.TARGET||"0x0240cF8bF1fC2636DA3a8d2a123439E78C2287e4";
const ABI=["function quote(uint8,uint256) view returns (uint256)","function owner() view returns (address)",
           "function activePhase(uint8) view returns (uint256)","function updatePhasePrice(uint8,uint256,uint256)"];
const KINDS=[["PremiumBuy",0],["PremiumSell",1]];
const execute=process.env.EXECUTE==="1";
async function main(){
  const [signer]=await ethers.getSigners();
  const one=ethers.parseUnits("1",18);
  const v1=new ethers.Contract(V1,ABI,ethers.provider);
  const c=new ethers.Contract(NEW,ABI,signer);
  console.log(`=== sync prices V1 → new swap — ${execute?"EXECUTE":"DRY RUN"} ===`);
  console.log("source V1:",V1,"\ntarget   :",NEW,"\nsigner   :",signer.address);
  const owner=await c.owner();
  if(owner.toLowerCase()!==signer.address.toLowerCase()){
    console.log("⚠️  signer is not the owner:",owner);
    if(execute) throw new Error("refusing: signer is not the owner");
  }
  const plan=[];
  for(const [name,k] of KINDS){
    const want=await v1.quote(k,one);
    const have=await c.quote(k,one);
    const idx=await c.activePhase(k);
    console.log(`\n${name}: V1 $${ethers.formatUnits(want,6)}  target $${ethers.formatUnits(have,6)}  (activePhase ${idx})`);
    if(want===have){ console.log("  already matches — skipping"); continue; }
    plan.push({name,k,idx,want});
    console.log(`  WILL SET → ${want} (raw 6dp) = $${ethers.formatUnits(want,6)}`);
  }
  if(!plan.length) return console.log("\nnothing to do.");
  if(!execute) return console.log("\nDRY RUN — nothing sent. Re-run with EXECUTE=1.");
  for(const p of plan){
    console.log(`\nsetting ${p.name}…`);
    const tx=await c.updatePhasePrice(p.k,p.idx,p.want);
    console.log("  tx:",tx.hash);
    const r=await tx.wait();
    if(r.status!==1) throw new Error("reverted");
  }
  console.log("\n--- post-state ---");
  let ok=true;
  for(const [name,k] of KINDS){
    const want=await v1.quote(k,one), have=await c.quote(k,one);
    console.log(`${name}: V1 $${ethers.formatUnits(want,6)}  target $${ethers.formatUnits(have,6)}  ${want===have?"MATCH":"MISMATCH"}`);
    if(want!==have) ok=false;
  }
  if(!ok) throw new Error("prices still differ");
  console.log("\nPRICES SYNCED.");
}
main().catch(e=>{console.error("\nFAILED:",e.message);process.exit(1)});
