package main

import (
	"bufio"
	"encoding/gob"
	"flag"
	"fmt"
	"os"

	"github.com/pkg/errors"
	"github.com/urfave/cli"
)

type deployment struct {
	BW2Entity     string
	URI           string
	Name          string
	Configuration string
	Timeout       string
}

func actionDeployLast(c *cli.Context) error {
	sourceFile := getDeploymentHistoryFile()
	oldDeployments, err := readDeployments(sourceFile)
	if err != nil {
		fmt.Printf("Failed to obtain deployment history: %s", err)
		os.Exit(1)
	}
	currentDir, err := os.Getwd()
	if err != nil {
		fmt.Printf("Failed to get current working directory: %s", err)
		os.Exit(1)
	}

	prevDeployment, ok := oldDeployments[currentDir]
	if !ok {
		fmt.Println("No previous deployment known for this directory")
		os.Exit(1)
	}

	var command string
	if len(prevDeployment.Timeout) > 0 {
		command = fmt.Sprintf("spawnctl -e %s deploy -u %s -c %s -n %s -t %s", prevDeployment.BW2Entity,
			prevDeployment.URI, prevDeployment.Configuration, prevDeployment.Name, prevDeployment.Timeout)
	} else {
		command = fmt.Sprintf("spawnctl -e %s deploy -u %s -c %s -n %s", prevDeployment.BW2Entity,
			prevDeployment.URI, prevDeployment.Configuration, prevDeployment.Name)
	}

	proceed := c.Bool("yes")
	if !proceed {
		fmt.Println("This will run:")
		fmt.Printf("\t%s\n", command)
		fmt.Println("Proceed? [Y/n]")
		reader := bufio.NewReader(os.Stdin)
		input, _ := reader.ReadString('\n')
		proceed = (input == "y\n" || input == "Y\n" || input == "\n")
	}

	if proceed {
		flags := flag.NewFlagSet("spawnctl", 0)
		flags.String("uri", prevDeployment.URI, "")
		flags.String("configuration", prevDeployment.Configuration, "")
		flags.String("name", prevDeployment.Name, "")
		flags.String("timeout", prevDeployment.Timeout, "")
		c.GlobalSet("entity", prevDeployment.BW2Entity)
		cliCtxt := cli.NewContext(c.App, flags, c)
		return actionDeploy(cliCtxt)
	}

	return nil
}

func getDeploymentHistoryFile() string {
	histFile := os.Getenv(deploymentHistVar)
	//Environment variable not set, fall back to default
	if len(histFile) == 0 {
		histFile = os.Getenv("HOME") + string(os.PathSeparator) + ".spawnpoint_history"
	}
	return histFile
}

func saveDeployment(directory string, dep deployment, fileName string) error {
	oldDeployments, err := readDeployments(fileName)
	if err != nil {
		return errors.Wrap(err, "Failed to read old deployments")
	}
	oldDeployments[directory] = dep
	if err = writeDeployments(oldDeployments, fileName); err != nil {
		return errors.Wrap(err, "Failed to write new deployment record")
	}
	return nil
}

func readDeployments(fileName string) (map[string]deployment, error) {
	var oldDeployments map[string]deployment
	reader, err := os.Open(fileName)
	if err != nil {
		if !os.IsNotExist(err) {
			return nil, errors.Wrap(err, "Failed to open history file")
		}
		oldDeployments = make(map[string]deployment)
		return oldDeployments, nil
	}

	defer reader.Close()
	decoder := gob.NewDecoder(reader)
	if err = decoder.Decode(&oldDeployments); err != nil {
		return nil, err
	}

	return oldDeployments, nil
}

func writeDeployments(deployments map[string]deployment, fileName string) error {
	writer, err := os.Create(fileName)
	if err != nil {
		return errors.Wrap(err, "Failed to create history file")
	}

	defer writer.Close()
	encoder := gob.NewEncoder(writer)
	if err = encoder.Encode(deployments); err != nil {
		return errors.Wrap(err, "Failed to encode deployments")
	}
	return nil
}
