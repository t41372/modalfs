package main

import (
	"bufio"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"github.com/t41372/modalfs/internal/encryption"
	"golang.org/x/term"
)

func readEncryptionKey() string {
	var raw []byte
	var err error
	if term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(os.Stderr, "Encryption key: ")
		raw, err = term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(os.Stderr)
	} else {
		var line string
		line, err = bufio.NewReader(os.Stdin).ReadString('\n')
		if errors.Is(err, io.EOF) && line != "" {
			err = nil
		}
		raw = []byte(line)
	}
	must(err)
	key, err := encryption.ParseKey(strings.TrimSpace(string(raw)))
	must(err)
	return key.Text()
}

func cmdEncryption(action string, args []string) {
	fl := flag.NewFlagSet(action, flag.ExitOnError)
	account := fl.String("account", "", "account label or id")
	env := fl.String("env", "", "Modal environment")
	remember := fl.Bool("remember", true, "remember the key on this computer; false keeps it only until lock or quit")
	pos := parseFlags(fl, args)
	if len(pos) != 1 {
		fatal(fmt.Errorf("usage: modalfs %s VOLUME [--account A] [--env E]", action))
	}
	c := client()
	a := pickAccount(state(c), *account)
	id := lookupVolume(c, a.ID, *env, pos[0]).ID
	in := map[string]any{"environment": *env}
	if action == "unlock" {
		in["key"], in["remember"] = readEncryptionKey(), *remember
	}
	must(c.Do("POST", "/api/accounts/"+a.ID+"/volumes/"+id+"/"+action, in, nil))
	fmt.Printf("%sed %s\n", action, pos[0])
}
