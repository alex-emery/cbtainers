package client

import (
	"context"
	"strings"

	"github.com/aemery-cb/cbtainers/pkg/util"
	"github.com/docker/docker/api/types"
)

func (cli *CBDockerClient) RebalanceCBCluster(ctx context.Context, containerID, username, password string) error {
	cli.logger.Info("rebalancing cluster")
	cmd := []string{
		"couchbase-cli",
		"rebalance",
		"-c",
		"127.0.0.1",
		"--username",
		username,
		"--password",
		password,
	}

	fn := func() error {
		_, err := cli.ExecCmd(ctx, containerID, cmd)
		return err
	}

	return util.WithRetry(fn)
}
func (cli *CBDockerClient) JoinCBCluster(ctx context.Context, containerID string, servers []string, username, password string) error {
	cli.logger.Info("Connecting clusters")
	cmd := []string{
		"couchbase-cli",
		"server-add",
		"--cluster", "127.0.0.1",
		"--username", username,
		"--password", password,
		"--server-add", strings.Join(servers, ","),
		"--server-add-username", username,
		"--server-add-password", password,
	}

	fn := func() error {
		_, err := cli.ExecCmd(ctx, containerID, cmd)
		return err
	}
	return util.WithRetry(fn)
}

func (cli *CBDockerClient) InitCBNode(ctx context.Context, container types.ContainerJSON, clusterName, username, password string) error {
	cli.logger.Infof("initialising container %s", container.Name)
	cmd := []string{
		"couchbase-cli",
		"cluster-init",
		"-c", "127.0.0.1",
		"--cluster-username", username,
		"--cluster-password", password,
		"--services", "data,index,query,fts,analytics",
		"--cluster-ramsize", "1024",
		"--cluster-index-ramsize", "512",
		"--cluster-eventing-ramsize", "512",
		"--cluster-fts-ramsize", "512",
		"--cluster-analytics-ramsize", "1024",
		"--cluster-fts-ramsize", "512",
		"--cluster-name", clusterName,
		"--index-storage-setting", "default",
	}

	fn := func() error {
		_, err := cli.ExecCmd(ctx, container.ID, cmd)
		return err
	}
	return util.WithRetry(fn)
}
