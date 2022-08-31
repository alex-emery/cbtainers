package test

import (
	"context"
	"fmt"
	"strings"
	"testing"

	"github.com/aemery-cb/cbtainers/pkg/client"
	"github.com/aemery-cb/cbtainers/pkg/cluster"
	"github.com/docker/docker/api/types"
)

func TestCreate(t *testing.T) {

	opts := cluster.Options{
		Username:  "Administrator",
		Password:  "Password",
		Size:      3,
		Prefix:    "cb-test",
		ImageName: "couchbase/server:7.1.1",
	}

	cluster, err := opts.Run()
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		for _, cleanup := range cluster.CleanUp {
			cleanup()
		}
	}()

	client, err := client.New()
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	// check cluster size
	containers, err := client.GetCBServerContainers(ctx, opts.Prefix)
	if err != nil {
		t.Fatal(err)
	}

	if len(containers) != opts.Size {
		t.Fatal(fmt.Errorf("incorrect cluster size expected: %d found: %d", opts.Size, len(containers)))
	}

	// check network
	network, err := client.NetworkInspect(ctx, cluster.Network.ID, types.NetworkInspectOptions{})
	if err != nil {
		t.Fatal(err)
	}

	if len(network.ID) == 0 {
		t.Fatal(fmt.Errorf("failed to find network %s", cluster.Network.ID))
	}

	// check cluster joined
	res, err := client.ExecCmd(ctx, containers[0].ID, []string{"couchbase-cli", "server-list", "-c", "127.0.0.1", "--username", opts.Username, "--password", opts.Password})
	if err != nil {
		t.Fatal(err)
	}
	tidy := strings.TrimSuffix(res, "\n")
	parts := strings.Split(tidy, "\n")
	if len(parts) != opts.Size {
		t.Fatal(fmt.Errorf("all nodes failed to join cluster. response: %s", res))
	}

}
