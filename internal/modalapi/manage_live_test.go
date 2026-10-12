//go:build integration

package modalapi

import (
	"context"
	"fmt"
	"testing"
	"time"
)

func TestManageLive(t *testing.T) {
	cfg, err := LoadConfig("")
	if err != nil {
		t.Skip(err)
	}
	c, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	id, err := c.WhoAmI(ctx)
	fmt.Printf("whoami: %+v err=%v\n", id, err)
	envs, err := c.ListEnvironments(ctx)
	fmt.Printf("envs: %+v err=%v\n", envs, err)
	name := fmt.Sprintf("modalfs-test-%d", time.Now().Unix())
	v, err := c.CreateVolume(ctx, name)
	if err != nil {
		t.Fatal("create:", err)
	}
	fmt.Printf("created %+v v2=%v\n", v, v.V2())
	if _, err := c.CreateVolume(ctx, name); err == nil {
		t.Fatal("expected duplicate create to fail")
	} else {
		fmt.Println("dup create err:", err)
	}
	if err := c.RenameVolume(ctx, v.ID, name+"-r"); err != nil {
		t.Fatal("rename:", err)
	}
	vs, _ := c.ListVolumes(ctx)
	found := false
	for _, x := range vs {
		if x.ID == v.ID {
			found = x.Name == name+"-r"
			fmt.Printf("listed %+v\n", x)
		}
	}
	if !found {
		t.Fatal("renamed volume not listed")
	}
	if err := c.DeleteVolume(ctx, v.ID); err != nil {
		t.Fatal("delete:", err)
	}
	if _, err := c.LookupVolume(ctx, name+"-r", false); err == nil {
		t.Fatal("still exists after delete")
	} else {
		fmt.Println("after delete lookup:", err, IsNotFound(err))
	}
}
