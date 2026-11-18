package meter_test

import (
	"testing"
	"time"

	"github.com/kiff/cloud/api/internal/meter"
	"github.com/kiff/cloud/api/internal/tooloutcome"
)

// Each case of the v1 meters: a call counts only when it got something
// done, and every free case is named.
func TestClassifyCountsOnlyWhatGotDone(t *testing.T) {
	at := time.Date(2026, 10, 22, 9, 0, 0, 0, time.UTC)
	rec := func(state, outcome string) tooloutcome.Record {
		return tooloutcome.Record{State: state, ToolOutcome: outcome, At: at}
	}
	for name, c := range map[string]struct {
		call    meter.Call
		counted bool
		free    string
		unit    meter.Unit
	}{
		"a taf's refund, sent and done":      {meter.Call{Taf: true, Record: rec("forwarded", "done")}, true, "", meter.UnitTask},
		"an own agent's call, sent and done": {meter.Call{Record: rec("forwarded", "done")}, true, "", meter.UnitAction},
		"held, approved, then sent and done": {meter.Call{Taf: true, ExceptionID: "exc_1", Record: rec("forwarded", "done")}, true, "", meter.UnitTask},
		"done, from an older result record":  {meter.Call{Taf: true, Record: tooloutcome.Record{State: "forwarded", HasResult: true, At: at}}, true, "", meter.UnitTask},
		"the tool answered with an error":    {meter.Call{Taf: true, Record: rec("forwarded", "error")}, false, meter.FreeFailed, meter.UnitTask},
		"the answer was lost":                {meter.Call{Taf: true, Record: rec("unknown", "")}, false, meter.FreeFailed, meter.UnitTask},
		"never sent":                         {meter.Call{Taf: true, Record: rec("failed", "")}, false, meter.FreeFailed, meter.UnitTask},
		"sent, how it ended not recorded":    {meter.Call{Taf: true, Record: rec("forwarded", "")}, false, meter.FreeFailed, meter.UnitTask},
		"a read":                             {meter.Call{Taf: true, ReadOnly: true, Record: rec("forwarded", "done")}, false, meter.FreeRead, meter.UnitTask},
		"Setup's read":                       {meter.Call{ReadOnly: true, SetupReader: true, Record: rec("forwarded", "done")}, false, meter.FreeSetup, meter.UnitAction},
		// Coach, Setup's agent, acting for the owner (kiff/tafs#206): not billed.
		"Setup's write, done":    {meter.Call{SetupReader: true, Record: rec("forwarded", "done")}, false, meter.FreeSetup, meter.UnitAction},
		"Setup's write, failed":  {meter.Call{SetupReader: true, Record: rec("forwarded", "error")}, false, meter.FreeSetup, meter.UnitAction},
		"Setup's write, refused": {meter.Call{SetupReader: true, Reasons: []string{"mandate_exceeded"}, Record: rec("refused", "")}, false, meter.FreeRefused, meter.UnitAction},
		"refused":                {meter.Call{Taf: true, Reasons: []string{"mandate_exceeded"}, Record: rec("refused", "")}, false, meter.FreeRefused, meter.UnitTask},
		"held, then refused":     {meter.Call{Taf: true, ExceptionID: "exc_1", Record: rec("refused", "")}, false, meter.FreeHeldRefused, meter.UnitTask},
		"halted":                 {meter.Call{Taf: true, Reasons: []string{"halted"}, Record: rec("refused", "")}, false, meter.FreeHalted, meter.UnitTask},
		"halted while held":      {meter.Call{Taf: true, ExceptionID: "exc_1", Reasons: []string{"halted"}, Record: rec("refused", "")}, false, meter.FreeHalted, meter.UnitTask},
		"invalid":                {meter.Call{Reasons: []string{"invalid_permit_request"}, Record: rec("refused", "")}, false, meter.FreeInvalid, meter.UnitAction},
	} {
		e, ok := meter.Classify(c.call)
		if !ok || e.Counted != c.counted || e.Free != c.free || e.Unit != c.unit || !e.At.Equal(at) {
			t.Errorf("%s: %+v (final %v); want counted %v, free %q, unit %s", name, e, ok, c.counted, c.free, c.unit)
		}
	}
	// A call that hasn't ended is left for later.
	for _, state := range []string{"deciding", "held", "forwarding"} {
		if _, ok := meter.Classify(meter.Call{Taf: true, Record: rec(state, "")}); ok {
			t.Errorf("%s was classified before it ended", state)
		}
	}
	if len(meter.FreeKinds) != 9 {
		t.Errorf("free kinds: %v", meter.FreeKinds)
	}
}
