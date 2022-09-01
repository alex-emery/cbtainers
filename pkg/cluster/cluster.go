package cluster

import (
	"context"
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
	Size       int
	Prefix     string
	DeleteOnly bool
}

type NotFoundError interface {
	NotFound()
}

type CouchbaseCluster struct {
	Nodes   []types.ContainerJSON
	Network types.NetworkResource
	CleanUp []func() error
}

func (opts *Options) Run() (*CouchbaseCluster, error) {

	cluster := &CouchbaseCluster{}

	ctx := context.Background()
	client, err := client.New()
	if err != nil {
		return nil, err
	}

	// clean up
	if err := client.CleanUpCBServers(ctx, opts.Prefix); err != nil {
		return nil, err
	}

	err = client.DeleteProxy(ctx)
	if err != nil {
		return nil, err
	}

	networkName := fmt.Sprintf("%s-network", opts.Prefix)
	clusterName := fmt.Sprintf("%s-cluster", opts.Prefix)

	err = client.NetworkRemove(ctx, networkName)
	if err != nil {
		if _, ok := err.(NotFoundError); !ok {
			return nil, err
		}
	}

	if opts.DeleteOnly {
		return nil, nil
	}

	// creation
	err = client.ImagePullAndWait(ctx, opts.ImageName, types.ImagePullOptions{})

	if err != nil {
		return nil, err
	}

	cbNetworkRes, err := client.NetworkCreate(ctx, networkName, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
	})
	if err != nil {
		return nil, err
	}

	cbNetwork, err := client.NetworkInspect(ctx, cbNetworkRes.ID, types.NetworkInspectOptions{})
	if err != nil {
		return nil, err
	}

	cluster.Network = cbNetwork
	cluster.CleanUp = append(cluster.CleanUp, func() error { return client.NetworkRemove(ctx, networkName) })

	containers := make([]types.ContainerJSON, 0)
	for i := 0; i < opts.Size; i += 1 {
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
			return nil, err
		}

		if err := client.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
			return nil, err
		}
		container, err := client.ContainerInspect(ctx, resp.ID)
		if err != nil {
			return nil, err
		}
		containers = append(containers, container)
	}

	if err != nil {
		return nil, err
	}

	cluster.Nodes = containers
	cluster.CleanUp = append(cluster.CleanUp, func() error {
		return client.CleanUpCBServers(ctx, opts.Prefix)
	})

	ipAddr := containers[0].NetworkSettings.Networks[networkName].IPAddress
	err = client.RunProxy(ctx, cbNetwork, "8091", ipAddr, "8091")
	if err != nil {
		return nil, err
	}

	err = util.WaitUntilReady("localhost")
	if err != nil {
		return nil, err
	}

	for _, container := range containers {
		err := client.InitCBNode(ctx, container.ID, clusterName, opts.Username, opts.Password)
		if err != nil {
			return nil, err
		}
	}

	var hostnames []string
	for _, container := range containers[1:] {
		hostnames = append(hostnames, strings.TrimPrefix(container.Name, "/"))
	}

	err = client.JoinCBCluster(ctx, containers[0].ID, hostnames, opts.Username, opts.Password)
	if err != nil {
		return nil, err
	}

	err = client.RebalanceCBCluster(ctx, containers[0].ID, opts.Username, opts.Password)
	if err != nil {
		return nil, err
	}

	return cluster, nil
}
