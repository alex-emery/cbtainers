package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/stdcopy"
)

func (cli *CBDockerClient) ExecCmd(ctx context.Context, containerID string, cmd []string) (string, error) {
	cresp, err := cli.ContainerExecCreate(ctx, containerID, types.ExecConfig{
		AttachStderr: true,
		AttachStdout: true,
		Cmd:          cmd,
	})
	if err != nil {
		return "", err
	}

	aresp, err := cli.ContainerExecAttach(ctx, cresp.ID, types.ExecStartCheck{})
	if err != nil {
		return "", err
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
			return "", err
		}
		break

	case <-ctx.Done():
		return "", ctx.Err()
	}

	// get the exit code
	iresp, err := cli.ContainerExecInspect(ctx, cresp.ID)
	if err != nil {
		return "", err
	}
	if iresp.ExitCode != 0 {
		return "", fmt.Errorf("command did not successfully exit: %s - code: %d\n stderr:%s", strings.Join(cmd, " "), iresp.ExitCode, errBuf.String())
	}

	return outBuf.String(), nil
}

func (cli *CBDockerClient) ImagePullAndWait(ctx context.Context, imageName string, opts types.ImagePullOptions) error {
	out, err := cli.ImagePull(ctx, imageName, types.ImagePullOptions{})
	if err != nil {
		return err
	}

	defer out.Close()
	// this blocks until the image has finished pulling.
	output, err := io.ReadAll(out)
	if err != nil {
		return err
	}
	cli.logger.Debug(output)
	return nil
}
