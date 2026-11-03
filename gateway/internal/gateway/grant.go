package gateway

// Run grants. A person's run carries the grant KIFF signed for them on
// each of its calls (KIFF-Run-Grant). The gateway passes it with each
// call it asks KIFF to decide, and KIFF checks it, in that one request:
// that it is for this key's agent, unexpired, and that the person it
// names may still use the agent. A grant that fails is a refusal, and
// nothing is sent. KIFF records the call, and any hold it opens, as the
// person's ("asked by"), so no one answers a hold they asked for.
//
// The gateway reads the grant and never trusts it: only KIFF can say who
// it is for. A call without one is decided as before.

// RunGrantHeader carries a person's run grant.
const RunGrantHeader = "KIFF-Run-Grant"
