package main

import (
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/immesys/wd"
	"github.com/urfave/cli"
	bw2 "gopkg.in/immesys/bw2bind.v5"
)

func main() {
	app := cli.NewApp()
	app.Name = "bw2Top"
	app.Usage = "Monitor for BW2 publications"
	app.Version = "0.1.0"
	app.Flags = []cli.Flag{
		cli.StringFlag{
			Name:   "entity",
			EnvVar: "BW2_DEFAULT_ENTITY",
		},
		cli.StringFlag{
			Name: "prefix",
		},
		cli.StringSliceFlag{
			Name: "endpoint",
		},
		cli.DurationFlag{
			Name:  "interval",
			Value: 2 * time.Minute,
		},
	}
	app.Action = runApp
	app.Run(os.Args)
}

func runApp(c *cli.Context) error {
	prefix := c.String("prefix")
	if prefix == "" {
		fmt.Println("Missing --prefix argument")
		os.Exit(1)
	}
	if !strings.HasSuffix(prefix, ".") {
		prefix += "."
	}
	entity := c.String("entity")
	if entity == "" {
		fmt.Println("Missing --entity argument")
		os.Exit(1)
	}
	wdTimeout := int(2 * c.Duration("interval"))

	uris := make([]string, len(c.StringSlice("endpoint")))
	wdNames := make([]string, len(c.StringSlice("endpoint")))
	for i, endpoint := range c.StringSlice("endpoint") {
		tokens := strings.SplitN(endpoint, ":", 2)
		if len(tokens) != 2 {
			fmt.Println("Invalid endpoint:", endpoint)
			os.Exit(1)
		}
		uris[i] = tokens[0]
		wdNames[i] = prefix + tokens[1]
	}

	bwClient := bw2.ConnectOrExit("")
	bwClient.SetEntityFileOrExit(c.String("entity"))

	var wg sync.WaitGroup
	wg.Add(len(uris))
	for i := 0; i < len(uris); i++ {
		go func() {
			defer wg.Done()

			pubCh, err := bwClient.Subscribe(&bw2.SubscribeParams{
				AutoChain: true,
				URI:       uris[i],
			})
			if err != nil {
				fmt.Println("Failed to subscribe to URI:", uris[i])
				return
			}
			for _ = range pubCh {
				wd.Kick(wdNames[i], wdTimeout)
				time.Sleep(c.Duration("interval"))
			}
		}()
	}
	wg.Wait()
	return nil
}
