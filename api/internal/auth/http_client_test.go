package auth

import "net/http"

// testHTTP is the auth tests' client, with a transport of its own. Not
// http.DefaultClient: the tests run in parallel, and httptest.Server.Close
// closes http.DefaultTransport's idle connections, so one test's server
// shutting down could break another test's reused connection mid-request
// ("http: CloseIdleConnections called").
var testHTTP = &http.Client{Transport: &http.Transport{}}
