package main

import (
	"testing"

	"github.com/t41372/modalfs/internal/config"
	"github.com/t41372/modalfs/internal/daemon"
)

// Logging in again leaves two accounts named after the same workspace. A
// name that fits both must be an error that tells them apart, never a guess.
func TestFindAccount(t *testing.T) {
	st := daemon.StateView{
		Accounts: []config.Account{
			{ID: "acct-old", Label: "acme", Workspace: "acme"},
			{ID: "acct-new", Label: "acme", Workspace: "acme"},
			{ID: "acct-home", Label: "home", Workspace: "personal"},
		},
		Mounts: []daemon.MountView{
			{Mount: config.Mount{ID: "mnt-1", AccountID: "acct-old"}},
			{Mount: config.Mount{ID: "mnt-2", AccountID: "acct-old"}, State: daemon.StateStopped},
			{Mount: config.Mount{ID: "mnt-3", AccountID: "acct-home"}},
		},
	}
	for _, tc := range []struct{ want, id, err string }{
		{want: "acct-new", id: "acct-new"},
		{want: "HOME", id: "acct-home"},
		{want: "personal", id: "acct-home"},
		{want: "acme", err: `2 accounts match "acme": acme (acct-old, 2 volumes), acme (acct-new, no volumes); use the account ID`},
		{want: "", err: "several accounts; pick one with --account: acme (acct-old, 2 volumes), acme (acct-new, no volumes), home (acct-home, 1 volume)"},
		{want: "work", err: `no account "work"`},
	} {
		a, err := findAccount(st, tc.want)
		if tc.err != "" {
			if err == nil || err.Error() != tc.err {
				t.Errorf("findAccount(%q) = %s, %v; want error %q", tc.want, a.ID, err, tc.err)
			}
		} else if err != nil || a.ID != tc.id {
			t.Errorf("findAccount(%q) = %s, %v; want %s", tc.want, a.ID, err, tc.id)
		}
	}

	// One account's label against another's workspace is ambiguous too.
	st.Accounts = []config.Account{{ID: "acct-a", Label: "acme", Workspace: "a"}, {ID: "acct-b", Label: "b", Workspace: "acme"}}
	if _, err := findAccount(st, "acme"); err == nil {
		t.Error("a label and another account's workspace: no error")
	}
	// An ID wins even over an earlier account named like it.
	st.Accounts = []config.Account{{ID: "acct-a", Label: "acct-b"}, {ID: "acct-b", Label: "b"}}
	if a, err := findAccount(st, "acct-b"); err != nil || a.ID != "acct-b" {
		t.Errorf("ID lost to a label: %s, %v", a.ID, err)
	}
	// With one account, no name is needed.
	st.Accounts = st.Accounts[:1]
	if a, err := findAccount(st, ""); err != nil || a.ID != "acct-a" {
		t.Errorf("single account: %s, %v", a.ID, err)
	}
}
