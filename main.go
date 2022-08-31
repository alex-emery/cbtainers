package main

import (
	"flag"

	"github.com/aemery-cb/cbtainers/pkg/cluster"
)

func main() {
	opts := cluster.Options{}

	flag.BoolVar(&opts.DeleteOnly, "delete", false, "deletes all only resources")
	flag.StringVar(&opts.ImageName, "image", "couchbase/server:7.1.1", "couchbase server image to be used")
	flag.IntVar(&opts.NumServers, "num", 3, "total number of server nodes")
	flag.StringVar(&opts.Username, "username", "Administrator", "Couchbase Server username")
	flag.StringVar(&opts.Password, "password", "password", "Couchbase Server password")
	flag.StringVar(&opts.Prefix, "prefix", "cb-server", "Prefix to be used for created Couchbase resources")
	flag.Parse()

	opts.Run()
}
