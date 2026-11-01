package main

import (
	"context"
	"flag"
	"github.com/Rashid2G/on-demand-vms/internal/worker"
	"log"
	"os"
	"os/signal"
	"syscall"

	"fmt"
	"github.com/Rashid2G/on-demand-vms/pkg/version"
)

func main() {
	if len(os.Args) >= 2 && (os.Args[1] == "version" || os.Args[1] == "-v" || os.Args[1] == "--version") {
		info := version.Get("compute-worker", "Compute Worker")
		fmt.Printf("%s\n\n", info.String())
		fmt.Print(info.Detailed(""))
		fmt.Println()
		return
	}
	if len(os.Args) < 2 || os.Args[1] != "run" {
		log.Fatal("usage: compute-worker run --control-plane URL --token TOKEN --name NAME --pool POOL")
	}
	f := flag.NewFlagSet("run", flag.ExitOnError)
	config := f.String("config", "", "config file")
	cp := f.String("control-plane", "", "control plane URL")
	token := f.String("token", "", "worker token")
	name := f.String("name", "", "worker name")
	pool := f.String("pool", "", "pool ID")
	_ = f.Parse(os.Args[2:])
	c, e := worker.LoadConfig(*config)
	if e != nil && *cp == "" {
		log.Fatal(e)
	}
	if *cp != "" {
		c.ControlPlaneURL = *cp
	}
	if *token != "" {
		c.Token = *token
	}
	if *name != "" {
		c.Name = *name
	}
	if *pool != "" {
		c.PoolID = *pool
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	if e = (&worker.Client{Config: c}).Run(ctx); e != nil {
		log.Fatal(e)
	}
}
