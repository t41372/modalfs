package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/t41372/modalfs/internal/daemon"
)

func cmdAccountLogin(c *daemon.Client, label, env string, noBrowser bool) error {
	var login daemon.LoginStatus
	if err := c.Do("POST", "/api/account-logins", map[string]string{"label": label, "environment": env}, &login); err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	path := "/api/account-logins/" + login.ID
	fmt.Println("Open this link in your browser to connect your Modal account:")
	fmt.Println(login.URL)
	if login.Code != "" {
		fmt.Println("Confirmation code:", login.Code)
	}
	fmt.Printf("This login expires at %s. Press Ctrl-C to cancel.\n", time.Unix(login.ExpiresAt, 0).Format(time.Kitchen))
	if !noBrowser {
		if err := c.Do("POST", path+"/open", nil, nil); err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			stop() // cancelling waits for a save under way; a second Ctrl-C quits at once
			if err := c.Do("DELETE", path, nil, &login); err != nil {
				return err
			}
		case <-tick.C:
			if err := c.Do("GET", path, nil, &login); err != nil {
				return err
			}
		}
		switch login.State {
		case "connected":
			fmt.Printf("connected %s (workspace %s, secret in %s)\n", login.Account.Label, login.Account.Workspace, login.Account.SecretStore)
			return nil
		case "error":
			return fmt.Errorf("%s", login.Error)
		case "cancelled":
			return fmt.Errorf("browser login was cancelled; run modalfs account login to try again")
		}
	}
}
