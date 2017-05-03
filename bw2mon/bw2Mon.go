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
			Name:   "agent",
			EnvVar: "BW2_AGENT",
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
	wdTimeout := 2 * int(c.Duration("interval")/time.Second)

	endpoints := c.StringSlice("endpoint")
	if len(endpoints) == 0 {
		fmt.Println("Missing --endpoint argument")
		os.Exit(1)
	}
	uris := make([]string, len(endpoints))
	wdNames := make([]string, len(endpoints))
	for i, endpoint := range endpoints {
		tokens := strings.SplitN(endpoint, ":", 2)
		if len(tokens) != 2 {
			fmt.Println("Invalid endpoint:", endpoint)
			os.Exit(1)
		}
		uris[i] = tokens[0]
		wdNames[i] = prefix + tokens[1]
	}

	bwClient := bw2.ConnectOrExit(c.String("agent"))
	bwClient.SetEntityFileOrExit(c.String("entity"))

	var wg sync.WaitGroup
	wg.Add(len(uris))
	for i := 0; i < len(uris); i++ {
		uri := uris[i]
		wdName := wdNames[i]
		go func() {
			defer wg.Done()

			pubCh, err := bwClient.Subscribe(&bw2.SubscribeParams{
				AutoChain: true,
				URI:       uri,
			})
			if err != nil {
				fmt.Printf("Failed to subscribe to URI '%s': %v", uri, err)
				return
			}

			for _ = range pubCh {
				kicked := wd.RLKick(c.Duration("interval"), wdName, wdTimeout)
				if kicked {
					fmt.Printf("Received message on URI %s, kicking watchdog %s\n", uri, wdName)
				}
			}
		}()
	}
	wg.Wait()
	return nil
}
