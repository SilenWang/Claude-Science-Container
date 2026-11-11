package main

import (
	"os"
	"testing"
)

// idleSession is a session that was never started: enough for the window's
// state changes, without opening a tunnel.
func idleSession(target Target) *session { return newSession(target) }

// Reloading keeps the tunnel of a target whose entry did not change, and
// drops the ones of targets that were edited or removed.
func TestReloadKeepsUnchangedSessions(t *testing.T) {
	path := writeTempConfig(t, `{
		"targets": [
			{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"},
			{"name": "beta", "ssh_host": "10.0.0.2", "ssh_key": "/k"}
		]
	}`)

	app := &guiApp{configPath: path}
	app.reload()

	alpha, beta := app.items[0], app.items[1]
	alphaSess, betaSess := idleSession(alpha.target), idleSession(beta.target)
	alpha.sess, alpha.phase = alphaSess, phaseConnected
	beta.sess, beta.phase = betaSess, phaseConnected

	// beta's host changes; alpha stays as it was.
	if err := os.WriteFile(path, []byte(`{
		"targets": [
			{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"},
			{"name": "beta", "ssh_host": "10.0.0.9", "ssh_key": "/k"}
		]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	app.reload()

	if len(app.items) != 2 {
		t.Fatalf("got %d cards, want 2", len(app.items))
	}
	if app.items[0] != alpha {
		t.Fatal("the unchanged card was replaced, dropping its tunnel")
	}
	if app.items[0].sess != alphaSess || app.items[0].phase != phaseConnected {
		t.Fatal("the unchanged card lost its session")
	}
	if app.items[1] == beta || app.items[1].sess != nil {
		t.Fatal("the edited card kept its old session")
	}
	if !betaSess.stopped() {
		t.Fatal("the edited card's session was not stopped")
	}
}

// A target removed from the config stops its tunnel.
func TestReloadStopsRemovedTargets(t *testing.T) {
	path := writeTempConfig(t, `{
		"targets": [
			{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"},
			{"name": "beta", "ssh_host": "10.0.0.2", "ssh_key": "/k"}
		]
	}`)

	app := &guiApp{configPath: path}
	app.reload()
	betaSess := idleSession(app.items[1].target)
	app.items[1].sess, app.items[1].phase = betaSess, phaseConnected

	if err := os.WriteFile(path, []byte(`{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`), 0o600); err != nil {
		t.Fatal(err)
	}
	app.reload()

	if len(app.items) != 1 {
		t.Fatalf("got %d cards, want 1", len(app.items))
	}
	if !betaSess.stopped() {
		t.Fatal("the removed target's session was not stopped")
	}
}

// A config that breaks stops every tunnel: the window can no longer show
// what they belong to.
func TestReloadStopsEverythingOnBrokenConfig(t *testing.T) {
	path := writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`)

	app := &guiApp{configPath: path}
	app.reload()
	sess := idleSession(app.items[0].target)
	app.items[0].sess, app.items[0].phase = sess, phaseConnected

	if err := os.WriteFile(path, []byte(`{ "targets": [ {"ssh_host": ""} ] }`), 0o600); err != nil {
		t.Fatal(err)
	}
	app.reload()

	if app.err == nil {
		t.Fatal("a broken config was accepted")
	}
	if len(app.items) != 0 {
		t.Fatalf("cards remain after a broken config: %d", len(app.items))
	}
	if !sess.stopped() {
		t.Fatal("the session was not stopped")
	}
}

// A session's report lands on its own card, and never on that of a session
// that replaced it.
func TestReportIgnoresReplacedSession(t *testing.T) {
	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`)}
	app.reload()

	it := app.items[0]
	stale, current := idleSession(it.target), idleSession(it.target)
	it.sess, it.phase = stale, phaseConnected
	it.sess = current // the card moved on to a new session

	app.report(it, stale, phaseError, "无法连接", "dial: refused")
	if it.phase != phaseConnected || it.detail != "" {
		t.Fatalf("a stale report reached the card: phase=%v detail=%q", it.phase, it.detail)
	}

	app.report(it, current, phaseError, "无法连接，3 秒后重试", "dial: refused")
	if it.phase != phaseError || it.detail != "dial: refused" {
		t.Fatalf("the current session's report was dropped: phase=%v detail=%q", it.phase, it.detail)
	}
}

// A success clears the failure text it replaces, so a card that recovers
// does not keep an error under a green dot.
func TestReportClearsDetailWhenConnected(t *testing.T) {
	app := &guiApp{configPath: writeTempConfig(t, `{
		"targets": [{"name": "alpha", "ssh_host": "10.0.0.1", "ssh_key": "/k"}]
	}`)}
	app.reload()

	it := app.items[0]
	s := idleSession(it.target)
	it.sess = s
	app.report(it, s, phaseError, "无法连接，3 秒后重试", "dial: refused")
	app.report(it, s, phaseConnected, "已连接", "")

	if it.detail != "" || it.showDetail {
		t.Fatalf("a connected card kept its failure text: %q (shown=%v)", it.detail, it.showDetail)
	}
	if it.phase != phaseConnected || it.status != "已连接" {
		t.Fatalf("state = %v %q", it.phase, it.status)
	}
}
