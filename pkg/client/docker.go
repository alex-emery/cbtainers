package client

import (
	"context"
	"fmt"
	"strings"
	"time"

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

func (cli *CBDockerClient) CleanUp(ctx context.Context, prefix string) error {

	errors := []string{}

	if err := cli.DeleteCBServers(ctx, prefix); err != nil {
		errors = append(errors, err.Error())
	}
	if err := cli.DeleteProxy(ctx); err != nil {
		errors = append(errors, err.Error())
	}
	if err := cli.DeleteCBNetworks(ctx, prefix); err != nil {
		errors = append(errors, err.Error())
	}

	if len(errors) != 0 {
		return fmt.Errorf("%s", strings.Join(errors, "\n"))
	}

	return nil
}

func (cli *CBDockerClient) CreateCBNetwork(ctx context.Context, networkName string) (types.NetworkResource, error) {
	cbNetworkRes, err := cli.NetworkCreate(ctx, networkName, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
	})
	if err != nil {
		return types.NetworkResource{}, err
	}

	cbNetwork, err := cli.NetworkInspect(ctx, cbNetworkRes.ID, types.NetworkInspectOptions{})
	if err != nil {
		return types.NetworkResource{}, err
	}

	return cbNetwork, nil
}

func (cli *CBDockerClient) CreateCBNodes(ctx context.Context, imgName string, prefix string, cbNet types.NetworkResource, size int) ([]types.ContainerJSON, error) {
	containers := make([]types.ContainerJSON, 0)
	for i := 0; i < size; i += 1 {
		name := fmt.Sprintf("%s-%d.docker", prefix, i)
		resp, err := cli.ContainerCreate(ctx, &container.Config{
			Image: imgName,
		}, nil, &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				cbNet.Name: {
					NetworkID: cbNet.ID,
				},
			},
		}, nil, name)
		if err != nil {
			return nil, err
		}

		if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
			return nil, err
		}
		container, err := cli.ContainerInspect(ctx, resp.ID)
		if err != nil {
			return nil, err
		}
		containers = append(containers, container)
	}

	return containers, nil
}

// Deletes all networks starting with the cluster prefix.
func (cli *CBDockerClient) DeleteCBNetworks(ctx context.Context, prefix string) error {
	networks, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		return err
	}
	errs := []string{}

	for _, network := range networks {
		if strings.HasPrefix(network.Name, prefix) {
			if err := cli.NetworkRemove(ctx, network.ID); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) != 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}

	return nil
}

// Deletes all docker containers that have the cluster prefix.
func (cli *CBDockerClient) DeleteCBServers(ctx context.Context, clusterPrefix string) error {
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

// Creates a container to act as a proxy, forwarding remote ports on another container to local ports on the host.
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
