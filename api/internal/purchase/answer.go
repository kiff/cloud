package purchase

import "strconv"

// AnswerLine is the line a held purchase's answer signs last, after
// kiff.answer.v1's own: the case version and digest the phone drew.
func AnswerLine(caseID string, version int, digest string) string {
	return "purchase:" + caseID + ":" + strconv.Itoa(version) + ":" + digest
}
