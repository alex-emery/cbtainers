package main

import (
	"context"
	"flag"
	"fmt"
	"strings"

	"github.com/aemery-cb/cbtainers/pkg/client"
	"github.com/aemery-cb/cbtainers/pkg/util"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
)

type Options struct {
	Username   string
	Password   string
	ImageName  string
	NumServers int
	Prefix     string
}

type NotFoundError interface {
	NotFound()
}

func main() {
	var deleteOnly bool

	opts := Options{}

	flag.BoolVar(&deleteOnly, "delete", false, "deletes all only resources")
	flag.StringVar(&opts.ImageName, "image", "couchbase/server:7.1.1", "couchbase server image to be used")
	flag.IntVar(&opts.NumServers, "num", 3, "total number of server nodes")
	flag.StringVar(&opts.Username, "username", "Administrator", "Couchbase Server username")
	flag.StringVar(&opts.Password, "password", "password", "Couchbase Server password")
	flag.StringVar(&opts.Prefix, "prefix", "cb-server", "Prefix to be used for created Couchbase resources")
	flag.Parse()

	ctx := context.Background()
	client, err := client.New()
	if err != nil {
		panic(err)
	}
	if err := client.CleanUpCBServers(ctx, opts.Prefix); err != nil {
		panic(err)
	}
	err = client.DeleteProxy(ctx)

	if err != nil {
		panic(err)
	}

	networkName := fmt.Sprintf("%s-network", opts.Prefix)
	clusterName := fmt.Sprintf("%s-cluster", opts.Prefix)

	err = client.NetworkRemove(ctx, networkName)
	if err != nil {
		if _, ok := err.(NotFoundError); !ok {
			panic(err)
		}
	}

	if deleteOnly {
		return
	}

	err = client.ImagePullAndWait(ctx, opts.ImageName, types.ImagePullOptions{})

	if err != nil {
		panic(err)
	}

	cbNetworkRes, err := client.NetworkCreate(ctx, networkName, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
	})
	if err != nil {
		panic(err)
	}

	cbNetwork, err := client.NetworkInspect(ctx, cbNetworkRes.ID, types.NetworkInspectOptions{})
	if err != nil {
		panic(err)
	}

	containers := make([]types.ContainerJSON, 0)
	for i := 0; i < opts.NumServers; i += 1 {
		name := fmt.Sprintf("%s-%d.docker", opts.Prefix, i)
		resp, err := client.ContainerCreate(ctx, &container.Config{
			Image: opts.ImageName,
		}, nil, &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				networkName: {
					NetworkID: cbNetwork.ID,
				},
			},
		}, nil, name)
		if err != nil {
			panic(err)
		}

		if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
			panic(err)
		}
		container, err := client.ContainerInspect(ctx, resp.ID)
		if err != nil {
			panic(err)
		}
		containers = append(containers, container)
	}

	if err != nil {
		panic(err)
	}

	ipAddr := containers[0].NetworkSettings.Networks[networkName].IPAddress
	err = client.RunProxy(ctx, cbNetwork, "8091", ipAddr, "8091")
	if err != nil {
		panic(err)
	}

	err = util.WaitUntilReady("localhost")
	if err != nil {
		panic(err)
	}

	for _, container := range containers {
		err := client.InitCBNode(ctx, container.ID, clusterName, opts.Username, opts.Password)
		if err != nil {
			panic(err)
		}
	}

	var hostnames []string
	for _, container := range containers[1:] {
		hostnames = append(hostnames, strings.TrimPrefix(container.Name, "/"))
	}

	err = client.JoinCBCluster(ctx, containers[0].ID, hostnames, opts.Username, opts.Password)
	if err != nil {
		panic(err)
	}
	fmt.Println("done")
}
