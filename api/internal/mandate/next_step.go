package mandate

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Refusal is what the person who refused a held action asked the agent to
// do instead: a next step the Card offers, such as "return_first",
// and an optional note in their own words. It is still a refusal: nothing
// is sent and the held call ends. The agent reads it when it collects the
// call, so "no" can say what to do next.
type Refusal struct {
	NextStep string `json:"next_step,omitempty"`
	Note     string `json:"note,omitempty"`
}

// Next-step bounds: a short list of named steps, not free text. The note is
// where free text goes.
const (
	MaxNextSteps    = 6
	MaxNextStepLen  = 40
	MaxRefusalNote  = 280
	nextStepPattern = `^[a-z][a-z0-9_]*$`
)

var nextStepName = regexp.MustCompile(nextStepPattern)

// ErrNextStepNotOnCard is a refusal naming a next step that none of the
// Cards the held action is outside of offers for its action.
var ErrNextStepNotOnCard = errors.New("mandate: that next step is not offered by the Card this action is held for")

// ErrInvalidRefusal is a note that is too long or carries control characters.
var ErrInvalidRefusal = errors.New("mandate: the refusal note is invalid")

func validateNextSteps(action string, steps []string) error {
	if len(steps) > MaxNextSteps {
		return fmt.Errorf("%w: action %s lists %d next steps; at most %d", ErrInvalidMandate, action, len(steps), MaxNextSteps)
	}
	for i, s := range steps {
		if len(s) > MaxNextStepLen || !nextStepName.MatchString(s) {
			return fmt.Errorf("%w: next step %q for %s must be lowercase letters, digits and underscores, starting with a letter, at most %d characters",
				ErrInvalidMandate, s, action, MaxNextStepLen)
		}
		if slices.Contains(steps[:i], s) {
			return fmt.Errorf("%w: next step %q is listed twice for %s", ErrInvalidMandate, s, action)
		}
	}
	return nil
}

// NextStepsFor lists the next steps a held action may be refused with: those
// offered by the matching grants of every Card it is outside of, in Card
// order, without repeats. Cards it does not name offer nothing.
func NextStepsFor(e Exception, cards []Mandate) []string {
	named := breachedIDs(e.Breaches)
	var out []string
	for _, m := range cards {
		if !named[m.ID] {
			continue
		}
		for _, g := range m.grantsFor(e.Action, e.Request.EntityState) {
			for _, s := range g.NextSteps {
				if !slices.Contains(out, s) {
					out = append(out, s)
				}
			}
		}
	}
	return out
}

// checkRefusal trims a refusal and checks it against the next steps on
// offer. A zero refusal is a plain "no", always allowed.
func checkRefusal(r Refusal, offered []string) (Refusal, error) {
	r.NextStep, r.Note = strings.TrimSpace(r.NextStep), strings.TrimSpace(r.Note)
	if r.NextStep != "" && !slices.Contains(offered, r.NextStep) {
		return Refusal{}, ErrNextStepNotOnCard
	}
	if utf8.RuneCountInString(r.Note) > MaxRefusalNote || strings.ContainsFunc(r.Note, func(c rune) bool {
		return unicode.IsControl(c) && c != '\n'
	}) {
		return Refusal{}, ErrInvalidRefusal
	}
	return r, nil
}

// refuseWith records a refusal on a held action that is being rejected.
func refuseWith(e *Exception, r Refusal, cards []Mandate) error {
	r, err := checkRefusal(r, NextStepsFor(*e, cards))
	if err != nil {
		return err
	}
	if r == (Refusal{}) {
		e.Refusal = nil
		return nil
	}
	e.Refusal = &r
	return nil
}
