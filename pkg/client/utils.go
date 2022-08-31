package client

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/pkg/stdcopy"
)

func (cli *CBDockerClient) ExecCmd(ctx context.Context, id string, cmd []string) error {
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

func (cli *CBDockerClient) ImagePullAndWait(ctx context.Context, imageName string, opts types.ImagePullOptions) error {
	out, err := cli.ImagePull(ctx, "verb/socat", types.ImagePullOptions{})
	if err != nil {
		return err
	}

	defer out.Close()
	// this blocks until the image has finished pulling.
	io.Copy(os.Stdout, out) //nolint:errcheck

	return nil
}
