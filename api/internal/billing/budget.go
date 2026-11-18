package billing

// MetaBudgetOps is the tenant-set monthly governed-operation budget
// (decimal string, e.g. "30000"). Absent or "0" means no budget. It
// never gates the runtime.
const MetaBudgetOps = "budget_ops"
