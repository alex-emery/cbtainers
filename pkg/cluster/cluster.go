package cluster

import (
	"context"
	"fmt"
	"strings"

	"github.com/docker/docker/api/types"
	"go.uber.org/zap"
)

type Options struct {
	Username   string
	Password   string
	ImageName  string
	Size       int
	Prefix     string
	DeleteOnly bool
	logger     *zap.SugaredLogger
}

type NotFoundError interface {
	NotFound()
}

type CouchbaseCluster struct {
	Nodes   []types.ContainerJSON
	Network types.NetworkResource
	cleanUp []func(context.Context) error
}

func (cluster *CouchbaseCluster) CleanUp(ctx context.Context) error {
	errs := []string{}
	for _, fn := range cluster.cleanUp {
		if err := fn(ctx); err != nil {
			errs = append(errs, err.Error())
		}
	}

	if len(errs) != 0 {
		return fmt.Errorf("%s", strings.Join(errs, "\n"))
	}

	return nil
}
