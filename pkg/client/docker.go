package client

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/aemery-cb/cbtainers/pkg/util"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	dockerClient "github.com/docker/docker/client"
	"github.com/docker/go-connections/nat"
	"go.uber.org/zap"
)

type CBDockerClient struct {
	*dockerClient.Client

	logger *zap.SugaredLogger
}

func New() (*CBDockerClient, error) {

	logger, _ := zap.NewProduction()
	sugar := logger.Sugar()
	cli, err := dockerClient.NewClientWithOpts(dockerClient.FromEnv, dockerClient.WithAPIVersionNegotiation())
	if err != nil {
		return nil, err
	}

	client := &CBDockerClient{Client: cli, logger: sugar}

	return client, nil
}

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

func (cli *CBDockerClient) GetCBServerContainers(ctx context.Context, clusterPrefix string) ([]types.Container, error) {
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	var cbContainers []types.Container

	for _, container := range containers {
		if strings.HasPrefix(container.Names[0], "/"+clusterPrefix) {
			cbContainers = append(cbContainers, container)
		}
	}

	return cbContainers, nil
}

// Deletes all docker containers that have the cluster prefix.
func (cli *CBDockerClient) CleanUpCBServers(ctx context.Context, clusterPrefix string) error {
	containers, err := cli.GetCBServerContainers(ctx, clusterPrefix)
	if err != nil {
		return err
	}

	for _, container := range containers {
		cli.logger.Infof("Removing %s %s", container.Names[0], container.Image)
		duration := 1 * time.Minute
		err := cli.ContainerStop(ctx, container.ID, &duration)
		if err != nil {
			return err
		}

		err = cli.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{RemoveVolumes: true})
		if err != nil {
			return err
		}
	}

	return nil
}

func (cli *CBDockerClient) DeleteProxy(ctx context.Context) error {
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return err
	}

	for _, container := range containers {
		if strings.HasPrefix(container.Names[0], "/cb-server-proxy") {
			cli.logger.Infof("Removing %s %s\n", container.Names[0], container.Image)
			duration := 1 * time.Minute
			err := cli.ContainerStop(ctx, container.ID, &duration)
			if err != nil {
				return err
			}

			err = cli.ContainerRemove(ctx, container.ID, types.ContainerRemoveOptions{RemoveVolumes: true})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func (cli *CBDockerClient) RunProxy(ctx context.Context, netwrk types.NetworkResource, localPort, targetIP, targetPort string) error {
	err := cli.ImagePullAndWait(ctx, "verb/socat", types.ImagePullOptions{})
	if err != nil {
		return err
	}

	socatPort := "8080"
	port, err := nat.NewPort("tcp", socatPort)
	if err != nil {
		return err
	}

	resp, err := cli.ContainerCreate(ctx, &container.Config{
		Image: "verb/socat",
		Cmd:   []string{fmt.Sprintf("TCP-LISTEN:%s,fork", socatPort), fmt.Sprintf("TCP-CONNECT:%s:%s", targetIP, targetPort)},
		ExposedPorts: nat.PortSet{
			port: {},
		},
	}, &container.HostConfig{
		NetworkMode: "default",
		PortBindings: map[nat.Port][]nat.PortBinding{
			port: {
				{
					HostPort: localPort,
				},
			},
		},
	}, &network.NetworkingConfig{
		EndpointsConfig: map[string]*network.EndpointSettings{
			netwrk.Name: {
				NetworkID: netwrk.ID,
			},
		},
	}, nil, "cb-server-proxy")
	if err != nil {
		return err
	}

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		return err
	}

	return nil
}
