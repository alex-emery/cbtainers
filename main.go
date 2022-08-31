package main

import (
	"bytes"
	"context"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
)

type Options struct {
	Username   string
	Password   string
	ImageName  string
	NumServers int
	Prefix     string
}

func WithRetry(fn func() error) error {
	var finalErr error
	for i := 0; i < 3; i += 1 {
		err := fn()
		if err == nil {
			return nil
		}
		finalErr = err
		time.Sleep(5 * time.Second)
	}
	return finalErr
}

func ExecCmd(ctx context.Context, cli *client.Client, id string, cmd []string) error {
	cresp, err := cli.ContainerExecCreate(ctx, id, types.ExecConfig{
		AttachStderr: true,
		AttachStdout: true,
		Cmd:          cmd,
	})
	if err != nil {
		return err
	}

	aresp, err := cli.ContainerExecAttach(ctx, cresp.ID, types.ExecStartCheck{})
	if err != nil {
		return err
	}
	// copied from https://github.com/moby/moby/blob/master/integration/internal/container/exec.go
	defer aresp.Close()

	var outBuf, errBuf bytes.Buffer
	outputDone := make(chan error, 1)

	go func() {
		_, err = stdcopy.StdCopy(&outBuf, &errBuf, aresp.Reader)
		outputDone <- err
	}()

	select {
	case err := <-outputDone:
		if err != nil {
			return err
		}
		break

	case <-ctx.Done():
		return ctx.Err()
	}

	// get the exit code
	iresp, err := cli.ContainerExecInspect(ctx, cresp.ID)
	if err != nil {
		return err
	}
	fmt.Println(outBuf.String())
	fmt.Println(errBuf.String())
	if iresp.ExitCode != 0 {
		return fmt.Errorf("command did not successfully exit: %d", iresp.ExitCode)
	}

	return nil
}

func JoinCBCluster(ctx context.Context, cli *client.Client, containerID string, servers []string, username, password string) error {
	fmt.Printf("Container ID %s is joining %s\n", containerID, strings.Join(servers, ", "))
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
		return ExecCmd(ctx, cli, containerID, cmd)
	}
	return WithRetry(fn)
}

func InitCBNode(ctx context.Context, cli *client.Client, id string, clusterName, username, password string) error {
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

	return ExecCmd(ctx, cli, id, cmd)
}

func WaitUntilReady(ipAddr string) error {
	var err error
	for retry := 0; retry < 3; retry += 1 {
		resp, err := http.Get("http://" + ipAddr + ":8091/ui/index.html")
		if err == nil && resp.StatusCode == 200 {
			return nil
		}
		time.Sleep(5 * time.Second)
	}
	return err
}

func GetCBServerContainers(ctx context.Context, cli *client.Client, clusterPrefix string) ([]types.Container, error) {
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

func CleanUpCBServers(ctx context.Context, cli *client.Client, clusterPrefix string) error {
	containers, err := GetCBServerContainers(ctx, cli, clusterPrefix)
	if err != nil {
		return err
	}

	for _, container := range containers {
		fmt.Printf("Removing %s %s\n", container.Names[0], container.Image)
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

func main() {
	var deleteOnly bool

	var opts = Options{}

	flag.BoolVar(&deleteOnly, "delete", false, "deletes all only resources")
	flag.StringVar(&opts.ImageName, "image", "couchbase/server:7.1.1", "couchbase server image to be used")
	flag.IntVar(&opts.NumServers, "num", 3, "total number of server nodes")
	flag.StringVar(&opts.Username, "username", "Administrator", "Couchbase Server username")
	flag.StringVar(&opts.Password, "password", "password", "Couchbase Server password")
	flag.StringVar(&opts.Prefix, "prefix", "cb-server", "Prefix to be used for created Couchbase resources")
	flag.Parse()

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	if err := CleanUpCBServers(ctx, cli, opts.Prefix); err != nil {
		panic(err)
	}
	err = DeleteProxy(ctx, cli)

	if err != nil {
		panic(err)
	}
	networks, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		panic(err)
	}

	networkName := fmt.Sprintf("%s-network", opts.Prefix)
	clusterName := fmt.Sprintf("%s-cluster", opts.Prefix)
	// check if the network exists before removing
	for _, net := range networks {
		if net.Name == networkName {
			err = cli.NetworkRemove(ctx, networkName)
			if err != nil {
				panic(err)
			}
		}
	}

	if deleteOnly {
		return
	}

	out, err := cli.ImagePull(ctx, opts.ImageName, types.ImagePullOptions{})

	if err != nil {
		panic(err)
	}

	defer out.Close()

	cbNetworkRes, err := cli.NetworkCreate(ctx, networkName, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
	})
	if err != nil {
		panic(err)
	}

	cbNetwork, err := cli.NetworkInspect(ctx, cbNetworkRes.ID, types.NetworkInspectOptions{})
	if err != nil {
		panic(err)
	}

	containers := make([]types.ContainerJSON, 0)
	for i := 0; i < opts.NumServers; i += 1 {
		name := fmt.Sprintf("%s-%d.docker", opts.Prefix, i)
		resp, err := cli.ContainerCreate(ctx, &container.Config{
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

		if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
			panic(err)
		}
		container, err := cli.ContainerInspect(ctx, resp.ID)
		if err != nil {
			panic(err)
		}
		containers = append(containers, container)
	}

	if err != nil {
		panic(err)
	}

	ipAddr := containers[0].NetworkSettings.Networks[networkName].IPAddress
	RunProxy(ctx, cli, cbNetwork, "8091", ipAddr, "8091")

	WaitUntilReady("localhost")

	for _, container := range containers {
		err := InitCBNode(ctx, cli, container.ID, clusterName, opts.Username, opts.Password)
		if err != nil {
			panic(err)
		}
	}

	var hostnames []string
	for _, container := range containers[1:] {
		hostnames = append(hostnames, strings.TrimPrefix(container.Name, "/"))
	}

	err = JoinCBCluster(ctx, cli, containers[0].ID, hostnames, opts.Username, opts.Password)
	if err != nil {
		panic(err)
	}
	fmt.Println("done")
}

func DeleteProxy(ctx context.Context, cli *client.Client) error {
	containers, err := cli.ContainerList(ctx, types.ContainerListOptions{})
	if err != nil {
		return err
	}

	for _, container := range containers {
		if strings.HasPrefix(container.Names[0], "/cb-server-proxy") {
			fmt.Printf("Removing %s %s\n", container.Names[0], container.Image)
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

func RunProxy(ctx context.Context, cli *client.Client, netwrk types.NetworkResource, localPort, targetIP, targetPort string) {
	out, err := cli.ImagePull(ctx, "verb/socat", types.ImagePullOptions{})
	if err != nil {
		panic(err)
	}

	defer out.Close()

	io.Copy(os.Stdout, out)
	socatPort := "8080"
	port, err := nat.NewPort("tcp", socatPort)
	if err != nil {
		panic(err)
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
		panic(err)
	}

	if err := cli.ContainerStart(ctx, resp.ID, types.ContainerStartOptions{}); err != nil {
		panic(err)
	}
}
