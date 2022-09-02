package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/aemery-cb/cbtainers/pkg/client"
	"github.com/aemery-cb/cbtainers/pkg/util"
	"github.com/docker/docker/api/types"
	"go.uber.org/zap"
)

func (opts *Options) Run() (*CouchbaseCluster, error) {
	logger, _ := zap.NewProduction()
	defer logger.Sync() // flushes buffer, if any
	opts.logger = logger.Sugar()

	networkName := fmt.Sprintf("%s-network", opts.Prefix)
	clusterName := fmt.Sprintf("%s-cluster", opts.Prefix)

	cluster := &CouchbaseCluster{}

	ctx := context.Background()
	client, err := client.New()
	if err != nil {
		return nil, err
	}

	// clean up
	client.CleanUp(ctx, opts.Prefix)

	if opts.DeleteOnly {
		return nil, nil
	}

	// creation
	err = client.ImagePullAndWait(ctx, opts.ImageName, types.ImagePullOptions{})

	if err != nil {
		return nil, err
	}

	cluster.Network, err = client.CreateCBNetwork(ctx, networkName)
	if err != nil {
		return nil, err
	}

	cluster.cleanUp = append(cluster.cleanUp, func(ctx context.Context) error { return client.NetworkRemove(ctx, networkName) })

	cluster.Nodes, err = client.CreateCBNodes(ctx, opts.ImageName, opts.Prefix, cluster.Network, opts.Size)
	if err != nil {
		return nil, err
	}

	cluster.cleanUp = append(cluster.cleanUp, func(ctx context.Context) error {
		return client.DeleteCBServers(ctx, opts.Prefix)
	})

	ipAddr := cluster.Nodes[0].NetworkSettings.Networks[networkName].IPAddress
	err = client.RunProxy(ctx, cluster.Network, "8091", ipAddr, "8091")
	if err != nil {
		return nil, err
	}

	err = util.WaitUntilReady("localhost")
	if err != nil {
		return nil, err
	}

	for _, container := range cluster.Nodes {
		err := client.InitCBNode(ctx, container, clusterName, opts.Username, opts.Password)
		if err != nil {
			return nil, err
		}
	}

	var hostnames []string
	for _, container := range cluster.Nodes[1:] {
		hostnames = append(hostnames, strings.TrimPrefix(container.Name, "/"))
	}

	err = client.JoinCBCluster(ctx, cluster.Nodes[0].ID, hostnames, opts.Username, opts.Password)
	if err != nil {
		return nil, err
	}

	err = client.RebalanceCBCluster(ctx, cluster.Nodes[0].ID, opts.Username, opts.Password)
	if err != nil {
		return nil, err
	}

	opts.logger.Info("done")
	return cluster, nil
}
