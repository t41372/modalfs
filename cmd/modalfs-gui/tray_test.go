package main

import (
	"encoding/json"
	"testing"
)

func state(t *testing.T, raw string) *trayState {
	t.Helper()
	var s trayState
	if err := json.Unmarshal([]byte(raw), &s); err != nil {
		t.Fatal(err)
	}
	return &s
}

func TestBuildTray(t *testing.T) {
	en := trayStrings["en"]
	if m := buildTray(nil, en); m.Summary != en.NotRunning || len(m.Volumes) != 0 {
		t.Errorf("no daemon: %+v", m)
	}

	m := buildTray(state(t, `{"mounts":[
		{"id":"a","volume_name":"datasets","state":"mounted","status":{"pending_files":0,"pending_bytes":0}},
		{"id":"b","volume_name":"archive","state":"stopped"}]}`), en)
	if m.Icon != "menubar" || m.Summary != "1 mounted · all on Modal" {
		t.Errorf("synced: %q %q", m.Icon, m.Summary)
	}
	if m.Volumes[0].Action != "reveal" || m.Volumes[1].Action != "mount" {
		t.Errorf("actions: %+v", m.Volumes)
	}

	m = buildTray(state(t, `{"mounts":[{"id":"a","volume_name":"datasets","state":"mounted","status":{"pending_files":2,"pending_bytes":1288490188}}],
		"downloads":[{"state":"running"},{"state":"paused"},{"state":"interrupted"}]}`), en)
	if m.Icon != "menubar-up" || m.Summary != "Uploading 1.2 GB" || m.Volumes[0].Detail != "1.2 GB to upload" {
		t.Errorf("uploading: %+v", m)
	}
	if m.Downloads != "2 remote downloads running" {
		t.Errorf("downloads: %q", m.Downloads)
	}

	// A problem outranks an upload.
	m = buildTray(state(t, `{"mounts":[
		{"id":"a","volume_name":"datasets","state":"mounted","status":{"pending_files":1,"pending_bytes":10}},
		{"id":"c","volume_name":"run-42","state":"mounted","open_conflicts":1}]}`), en)
	if m.Icon != "menubar-attn" || m.Summary != "1 needs a look" || m.Volumes[1].Action != "volume" {
		t.Errorf("attention: %+v", m)
	}
}

func TestResolveLang(t *testing.T) {
	for _, c := range []struct{ setting, system, want string }{
		{"en", "zh-Hant-TW", "en"},
		{"system", "zh-Hant-TW", "zh-Hant"},
		{"", "zh-TW", "zh-Hant"},
		{"", "zh-Hans-CN", "zh-Hans"},
		{"system", "de-DE", "en"},
		{"zh-Hans", "en-US", "zh-Hans"},
	} {
		if got := resolveLang(c.setting, c.system); got != c.want {
			t.Errorf("resolveLang(%q, %q) = %q, want %q", c.setting, c.system, got, c.want)
		}
	}
	for lang, s := range trayStrings {
		if s.Quit == "" || menuLabels[lang]["quit"] == "" {
			t.Errorf("%s: missing words", lang)
		}
	}
}
