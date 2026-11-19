package candidate

import "github.com/kiff/cloud/api/internal/guard"

// derive.go promotes the spike's derivation core (DeriveFromRuntime +
// Validate) into a single production-callable entry point. The API's
// GET /v1/guard/candidates handler calls DeriveForObservation directly;
// it does NOT depend on cmd/spike-candidate (the dev CLI) or on the
// on-disk fixtures, which remain dev-only.
//
// The honesty boundary is unchanged: candidates are grounded in the stored
// runtime observation, evidence cites the runtime, and policy thresholds /
// states are always null. Validate is reused verbatim as the grounding gate.

// RuntimeRef is the runtime coordinate a candidate set was derived from —
// the same key the observation is stored under (tenant is implicit in the
// authenticated request).
type RuntimeRef struct {
	AgentID     string `json:"agent_id"`
	Adapter     string `json:"adapter"`
	Project     string `json:"project"`
	Environment string `json:"environment"`
	Workflow    string `json:"workflow"`
	Mode        string `json:"mode"`
}

// RuntimeCandidates is the derived, grounded candidate set for one
// connected runtime: the runtime it came from, the ranked candidate
// controls, and the structured grounding report. Stable shape for Studio.
type RuntimeCandidates struct {
	Runtime    RuntimeRef `json:"runtime"`
	Candidates []Control  `json:"candidates"`
	Grounding  Report     `json:"grounding"`
}

// DeriveForObservation derives candidate controls for one connected runtime
// from its stored observation and grounds them. This is the production
// seam: deterministic, offline, no model call, no network. Connection
// supplies the evidence binding (adapter / project / environment / workflow
// / mode); Observation supplies the tools.
func DeriveForObservation(conn guard.Connection, obs guard.Observation) RuntimeCandidates {
	controls := DeriveFromRuntime(conn, obs)
	if controls == nil {
		controls = []Control{}
	}
	return RuntimeCandidates{
		Runtime: RuntimeRef{
			AgentID:     conn.AgentID,
			Adapter:     conn.Adapter,
			Project:     conn.Project,
			Environment: conn.Environment,
			Workflow:    conn.Workflow,
			Mode:        conn.Mode,
		},
		Candidates: controls,
		Grounding:  Validate(obs, controls),
	}
}
