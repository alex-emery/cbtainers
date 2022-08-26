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
	NetworkName string
	Username    string
	Password    string
	ClusterName string
	ImageName   string
	NumServers  int
}

var opts = Options{
	NetworkName: "cb-server-network",
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

func ExecCmd(cli *client.Client, id string, cmd []string) error {
	ctx := context.Background()
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

func JoinCBCluster(cli *client.Client, containerID string, servers []string) error {
	fmt.Printf("Container ID %s is joining %s\n", containerID, strings.Join(servers, ", "))
	cmd := []string{
		"couchbase-cli",
		"server-add",
		"--cluster", "127.0.0.1",
		"--username", opts.Username,
		"--password", opts.Password,
		"--server-add", strings.Join(servers, ","),
		"--server-add-username", opts.Username,
		"--server-add-password", opts.Password,
	}

	fn := func() error {
		return ExecCmd(cli, containerID, cmd)
	}
	return WithRetry(fn)
}

func InitCBNode(cli *client.Client, id string) error {
	cmd := []string{
		"couchbase-cli",
		"cluster-init",
		"-c", "127.0.0.1",
		"--cluster-username", opts.Username,
		"--cluster-password", opts.Password,
		"--services", "data,index,query,fts,analytics",
		"--cluster-ramsize", "1024",
		"--cluster-index-ramsize", "512",
		"--cluster-eventing-ramsize", "512",
		"--cluster-fts-ramsize", "512",
		"--cluster-analytics-ramsize", "1024",
		"--cluster-fts-ramsize", "512",
		"--cluster-name", opts.ClusterName,
		"--index-storage-setting", "default",
	}

	return ExecCmd(cli, id, cmd)
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

func GetCBServerContainers(cli *client.Client) ([]types.Container, error) {
	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return nil, err
	}

	var cbContainers []types.Container

	for _, container := range containers {
		if strings.HasPrefix(container.Names[0], "/"+opts.ClusterName) {
			cbContainers = append(cbContainers, container)
		}
	}

	return cbContainers, nil
}

func CleanUpCBServers(cli *client.Client) error {
	containers, err := GetCBServerContainers(cli)
	if err != nil {
		return err
	}

	for _, container := range containers {
		if strings.HasPrefix(container.Names[0], "/"+opts.ClusterName) {
			fmt.Printf("Removing %s %s\n", container.Names[0], container.Image)
			duration := 1 * time.Minute
			err := cli.ContainerStop(context.Background(), container.ID, &duration)
			if err != nil {
				return err
			}

			err = cli.ContainerRemove(context.Background(), container.ID, types.ContainerRemoveOptions{RemoveVolumes: true})
			if err != nil {
				return err
			}
		}
	}

	return nil
}

func main() {
	var deleteOnly bool
	flag.BoolVar(&deleteOnly, "delete", false, "deletes all only resources")
	flag.StringVar(&opts.ImageName, "image", "couchbase/server:7.1.1", "couchbase server image to be used")
	flag.IntVar(&opts.NumServers, "num", 3, "total number of server nodes")
	flag.StringVar(&opts.Username, "username", "Administrator", "Couchbase Server username")
	flag.StringVar(&opts.Password, "password", "password", "Couchbase Server password")
	flag.StringVar(&opts.NetworkName, "network-name", "cb-server-network", "Couchbase Server Network")
	flag.StringVar(&opts.ClusterName, "cluster-name", "cb-cluster", "Couchbase Server Network")
	flag.Parse()

	ctx := context.Background()
	cli, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		panic(err)
	}

	if err := CleanUpCBServers(cli); err != nil {
		panic(err)
	}
	err = DeleteProxy(cli)

	if err != nil {
		panic(err)
	}
	networks, err := cli.NetworkList(ctx, types.NetworkListOptions{})
	if err != nil {
		panic(err)
	}

	for _, net := range networks {
		if net.Name == opts.NetworkName {
			err = cli.NetworkRemove(context.Background(), opts.NetworkName)
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

	io.Copy(os.Stdout, out)
	out, err = cli.ImagePull(ctx, "verb/socat", types.ImagePullOptions{})
	if err != nil {
		panic(err)
	}

	io.Copy(os.Stdout, out)
	cbNetwork, err := cli.NetworkCreate(context.Background(), opts.NetworkName, types.NetworkCreate{
		CheckDuplicate: true,
		Driver:         "bridge",
	})
	if err != nil {
		panic(err)
	}

	defer out.Close()
	io.Copy(os.Stdout, out)

	containers := make([]types.ContainerJSON, 0)
	for i := 0; i < opts.NumServers; i += 1 {
		name := fmt.Sprintf("%s-%d.docker", opts.ClusterName, i)
		resp, err := cli.ContainerCreate(ctx, &container.Config{
			Image: opts.ImageName,
		}, nil, &network.NetworkingConfig{
			EndpointsConfig: map[string]*network.EndpointSettings{
				opts.NetworkName: {
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

	ipAddr := containers[0].NetworkSettings.Networks[opts.NetworkName].IPAddress
	RunProxy(cli, cbNetwork.ID, "8091", ipAddr, "8091")

	WaitUntilReady("localhost")

	for _, container := range containers {
		err := InitCBNode(cli, container.ID)
		if err != nil {
			panic(err)
		}
	}

	var hostnames []string
	for _, container := range containers[1:] {
		hostnames = append(hostnames, strings.TrimPrefix(container.Name, "/"))
	}

	err = JoinCBCluster(cli, containers[0].ID, hostnames)
	if err != nil {
		panic(err)
	}
	fmt.Println("done")
}

func DeleteProxy(cli *client.Client) error {
	containers, err := cli.ContainerList(context.Background(), types.ContainerListOptions{})
	if err != nil {
		return err
	}

	for _, container := range containers {
		if strings.HasPrefix(container.Names[0], "/cb-server-proxy") {
			fmt.Printf("Removing %s %s\n", container.Names[0], container.Image)
			duration := 1 * time.Minute
			err := cli.ContainerStop(context.Background(), container.ID, &duration)
			if err != nil {
				return err
			}

			err = cli.ContainerRemove(context.Background(), container.ID, types.ContainerRemoveOptions{RemoveVolumes: true})
			if err != nil {
				return err
			}
		}
	}
	return nil
}

func RunProxy(cli *client.Client, networkID string, localPort, targetIP, targetPort string) {
	socatPort := "8080"
	port, err := nat.NewPort("tcp", socatPort)
	if err != nil {
		panic(err)
	}

	resp, err := cli.ContainerCreate(context.Background(), &container.Config{
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
			opts.NetworkName: {
				NetworkID: networkID,
			},
		},
	}, nil, "cb-server-proxy")
	if err != nil {
		panic(err)
	}

	if err := cli.ContainerStart(context.Background(), resp.ID, types.ContainerStartOptions{}); err != nil {
		panic(err)
	}
}
