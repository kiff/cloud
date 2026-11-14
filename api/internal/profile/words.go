package profile

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/kiff/cloud/api/internal/permit"
)

// WordsVersion names the renderer. A change to any line's text is a new
// version: the words an owner signed must render the same forever.
const WordsVersion = "kiff.words.v1"

// WordsVersion2 is the owner's words without KIFF's own bookkeeping: no
// line that KIFF checks a vendor daily, and the lost-answer line only on
// operations that move or record money, where the owner is the one asked.
// v1 renders as it always has; an app reads v2 only when it asks for it.
const WordsVersion2 = "kiff.words.v2"

// WordsVersions are the renderers KIFF answers, oldest first.
var WordsVersions = []string{WordsVersion, WordsVersion2}

// Words is what the owner reads and approves for a profile. The apps
// show each line's text as it is, in order; KIFF renders it, never Coach.
type Words struct {
	V     string `json:"v"`
	Lines []Line `json:"lines"`
}

// Line is one sentence. Op is the operation it is about, empty for the
// connection. Kind lets an app group or style lines; the text stands on
// its own.
type Line struct {
	Op   string `json:"op,omitempty"`
	Kind string `json:"kind"`
	Text string `json:"text"`
}

// Line kinds.
const (
	KindDoes      = "does"      // what the operation does
	KindOutside   = "outside"   // it can reach people outside the company
	KindScope     = "scope"     // what a Card's "Only for" can name
	KindCounts    = "counts"    // how its amount is counted
	KindChecks    = "checks"    // what KIFF checks after a call
	KindMissing   = "missing"   // what KIFF can't tell, and what it does instead
	KindNoEffect  = "no_effect" // refusals that give the Card its room back
	KindProbation = "probation" // outcomes the owner confirms first
	KindUnpinned  = "unpinned"  // the vendor's version isn't pinned
)

// ProbationCalls is how many outcomes of each money write the owner
// confirms on a profile no test call proved.
const ProbationCalls = 3

// Render returns the words for a valid profile, in kiff.words.v1.
func Render(p Profile) Words { return render(p, WordsVersion) }

// RenderVersion returns the words for a valid profile in version v, one of
// WordsVersions; ok is false for any other.
func RenderVersion(p Profile, v string) (w Words, ok bool) {
	for _, known := range WordsVersions {
		if v == known {
			return render(p, v), true
		}
	}
	return Words{}, false
}

func render(p Profile, version string) Words {
	v2 := version == WordsVersion2
	w := Words{V: version}
	add := func(op, kind, text string) { w.Lines = append(w.Lines, Line{Op: op, Kind: kind, Text: text}) }
	v := p.Vendor
	for _, o := range p.Operations {
		t := o.Title
		switch {
		case o.Kind == "read":
			add(o.Tool, KindDoes, t+": reads from "+v+", changes nothing.")
		case o.Effect == EffectDraft:
			add(o.Tool, KindDoes, t+": makes a draft in "+v+". Nothing leaves until it's approved.")
		case o.Effect == EffectSends:
			add(o.Tool, KindDoes, t+": sends a message through "+v+".")
		case o.Effect == EffectWrites:
			add(o.Tool, KindDoes, t+": writes in "+v+".")
		case o.Effect == EffectRecordsMoney:
			add(o.Tool, KindDoes, t+": records money in "+v+". It doesn't pay out or charge.")
		case o.Effect == EffectMovesMoney:
			add(o.Tool, KindDoes, t+": moves money through "+v+". It pays out or charges.")
		}
		if o.Shape == "batch" {
			add(o.Tool, KindDoes, t+": can act on several at once.")
		}
		if r := o.Resource; r != nil {
			if r.Outside {
				add(o.Tool, KindOutside, t+": can reach people outside your company.")
			}
		}
		if o.Kind != "write" {
			continue
		}
		if r := o.Resource; r != nil {
			add(o.Tool, KindScope, t+`: a Card's "Only for" can name which `+r.Noun+" it acts on.")
		}
		if a := o.Amount; a != nil {
			add(o.Tool, KindCounts, t+": "+counted(*a, v))
		}
		if vr := o.VerifyRead; vr != nil {
			if vr.AmountPath != "" {
				add(o.Tool, KindChecks, t+": after each call, KIFF reads it back from "+v+" to check the amount.")
			} else {
				add(o.Tool, KindChecks, t+": after each call, KIFF reads it back from "+v+" to check it took effect.")
			}
		} else if o.Money() {
			add(o.Tool, KindMissing, t+": "+v+" doesn't say back what it used, so KIFF counts what the taf asked for.")
		}
		if s := o.StatusRead; v2 && (s == nil || s.None != "") {
			// The owner's part, said as the owner's: only where money is.
			if o.Money() {
				add(o.Tool, KindMissing, t+": if "+v+"'s answer is lost, you confirm whether it went through; until then it stays counted.")
			}
		} else if s == nil || s.None != "" {
			if o.Money() {
				add(o.Tool, KindMissing, t+": if an answer is lost, KIFF can't check whether it happened, so it stays counted until you say.")
			} else {
				add(o.Tool, KindMissing, t+": if an answer is lost, KIFF can't check whether it happened, so it stays counted.")
			}
		}
		if n := o.NoEffect; n != nil {
			add(o.Tool, KindNoEffect, t+": when "+v+" refuses it with "+statuses(n.Statuses)+", the Card gets that room back. "+v+"'s docs say nothing happened then.")
		}
		if o.Money() && !p.TestProven {
			add(o.Tool, KindProbation, t+": not tried with a test account, so you confirm its first "+strconv.Itoa(ProbationCalls)+" outcomes.")
		}
	}
	if p.Pin.Kind == "none" && !v2 {
		what := "API"
		if p.Transport == "mcp" {
			what = "server"
		}
		add("", KindUnpinned, v+"'s "+what+" isn't pinned to a version, so KIFF checks it daily.")
	}
	return w
}

func counted(a Amount, vendor string) string {
	in := "in " + a.Unit
	if a.UnitRead != nil {
		in = "in the currency " + vendor + " reports"
	}
	s := "counted " + in
	if a.From == "read" {
		s += ", from what " + vendor + " says the amount is"
	}
	if a.Format == "decimal_string" {
		s += fmt.Sprintf(", read as a decimal with up to %d places", a.Scale)
	}
	return s + "."
}

// statuses writes 402 or 404, or 400, 402 or 404.
func statuses(ss []int) string {
	parts := make([]string, len(ss))
	for i, s := range ss {
		parts[i] = strconv.Itoa(s)
	}
	if len(parts) == 1 {
		return parts[0]
	}
	return strings.Join(parts[:len(parts)-1], ", ") + " or " + parts[len(parts)-1]
}

// WordsDigest is the hex SHA-256 of the words' RFC 8785 (JCS) form: the
// words_sha256 the owner's approval carries.
func WordsDigest(w Words) (string, error) {
	b, err := json.Marshal(w)
	if err != nil {
		return "", err
	}
	return permit.ArgsSHA256(b)
}
