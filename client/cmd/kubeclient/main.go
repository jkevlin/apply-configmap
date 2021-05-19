package main

import (
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/hashicorp/go-hclog"
	"github.com/jkevlin/apply-configmap/client"
)

var namespace string
var configmapName string
var yamlFilename string

func init() {
	flag.StringVar(&namespace, "namespace", "", "the namespace to use")
	flag.StringVar(&configmapName, "configmap-name", "", "the configmap name to use")
	flag.StringVar(&yamlFilename, "file", "", "the configmap file name to apply")
}

func main() {
	flag.Parse()

	c, err := client.New(hclog.Default())
	if err != nil {
		panic(err)
	}

	reqCh := make(chan struct{})
	shutdownCh := makeShutdownCh()

	go func() {
		defer close(reqCh)

		err = c.ApplyConfigMap(namespace, yamlFilename)
		if err != nil {
			panic(err)
		}
		return
	}()

	select {
	case <-shutdownCh:
		fmt.Println("Interrupt received, exiting...")
	case <-reqCh:
	}
}

func makeShutdownCh() chan struct{} {
	resultCh := make(chan struct{})

	shutdownCh := make(chan os.Signal, 4)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-shutdownCh
		close(resultCh)
	}()
	return resultCh
}
