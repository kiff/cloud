// Package apps is the list of apps tafs connects: the single place the site
// names them. /tafs and the docs render from it, so a page never lists an app
// by hand, and never lists one as working that doesn't connect today.
//
// apps.json holds one entry per app: how it connects (an API key, or a
// sign-in KIFF manages) and where it stands: it connects with its API key
// (the connect matrix reads its description and takes its key), a managed
// sign-in is coming, or no way to connect is confirmed yet. With
// KIFF_APPS_PROVED on, which apps work comes from planque's reviewed proofs
// instead (proved.go), so the site and tafs' gallery read one source.
package apps

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"sort"
)

//go:embed apps.json
var raw []byte

// How an app connects.
const (
	ConnectsKey    = "key"
	ConnectsSignIn = "sign-in"
)

// Where an app stands.
const (
	// StatusWorks: it connects today, by its API key as the connect matrix
	// checks, or by a sign-in a tester completed on a phone.
	StatusWorks = "works"
	// StatusComing: a managed sign-in, not live yet.
	StatusComing = "coming"
	// StatusNotYet: no way to connect is confirmed; it has no method.
	StatusNotYet = "not-yet"
)

// App is one app tafs connects.
type App struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Connects string `json:"connects,omitempty"`
	Status   string `json:"status"`
}

// Works reports whether the app connects with its API key today.
func (a App) Works() bool { return a.Status == StatusWorks }

// Label is the app's status in words, as the site says it.
func (a App) Label() string {
	switch a.Status {
	case StatusWorks:
		if a.Connects == ConnectsSignIn {
			return "Connects with a sign-in"
		}
		return "Connects with its API key"
	case StatusComing:
		return "Coming"
	}
	return "Not yet"
}

var list []App

func init() {
	var f struct {
		Apps []App `json:"apps"`
	}
	if err := json.Unmarshal(raw, &f); err != nil {
		panic("apps: " + err.Error())
	}
	if err := validate(f.Apps); err != nil {
		panic("apps: " + err.Error())
	}
	list = f.Apps
	embedded := append([]App(nil), list...)
	current.Store(&embedded)
}

// validate refuses a list the site can't render honestly: a duplicate or
// empty id, an unknown status, an app that works or is coming without a
// way to connect, or one not yet with a way it doesn't have.
func validate(as []App) error {
	seen := map[string]bool{}
	for _, a := range as {
		if a.ID == "" || a.Name == "" {
			return fmt.Errorf("an app needs an id and a name: %+v", a)
		}
		if seen[a.ID] {
			return fmt.Errorf("%s is listed twice", a.ID)
		}
		seen[a.ID] = true
		switch a.Status {
		case StatusWorks, StatusComing:
			if a.Connects != ConnectsKey && a.Connects != ConnectsSignIn {
				return fmt.Errorf("%s: connects %q is not %q or %q", a.ID, a.Connects, ConnectsKey, ConnectsSignIn)
			}
		case StatusNotYet:
			if a.Connects != "" {
				return fmt.Errorf("%s is not yet, so it names no way to connect (%q)", a.ID, a.Connects)
			}
		default:
			return fmt.Errorf("%s: status %q is not %q, %q or %q", a.ID, a.Status, StatusWorks, StatusComing, StatusNotYet)
		}
	}
	return nil
}

// All is every app, works first, then by name: apps.json, or with
// KIFF_APPS_PROVED on, apps.json under planque's proofs (proved.go).
func All() []App {
	out := append([]App(nil), effective()...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Works() != out[j].Works() {
			return out[i].Works()
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// ByID is the app with this id.
func ByID(id string) (App, bool) {
	for _, a := range effective() {
		if a.ID == id {
			return a, true
		}
	}
	return App{}, false
}

// Working is the apps that connect today, by name.
func Working() []App {
	var out []App
	for _, a := range All() {
		if a.Works() {
			out = append(out, a)
		}
	}
	return out
}

// Coming is the apps that don't connect yet (coming or not yet), by name.
func Coming() []App {
	var out []App
	for _, a := range All() {
		if !a.Works() {
			out = append(out, a)
		}
	}
	return out
}
