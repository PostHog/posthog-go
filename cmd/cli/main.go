package main

import (
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/posthog/posthog-go"
	"github.com/urfave/cli"
)

func main() {
	var apiKey string
	var operationType string
	var distinctId string
	var event string
	var properties string
	var alias string

	app := cli.NewApp()
	app.Name = "posthog"
	app.Usage = "Call the PostHog API"

	app.Flags = []cli.Flag{
		&cli.StringFlag{
			Name:        "apiKey",
			Usage:       "The PostHog API Key",
			Destination: &apiKey,
		},
		&cli.StringFlag{
			Name:        "type",
			Usage:       "The type of the message to send",
			Destination: &operationType,
		},
		&cli.StringFlag{
			Name:        "distinctId",
			Usage:       "Distinct ID for the user",
			Destination: &distinctId,
		},
		&cli.StringFlag{
			Name:        "event",
			Usage:       "Name of the captured event",
			Destination: &event,
		},
		&cli.StringFlag{
			Name:        "properties",
			Usage:       "Metadata associated with an event or identify call",
			Destination: &properties,
		},
		&cli.StringFlag{
			Name:        "alias",
			Usage:       "Alias for distinctId",
			Destination: &alias,
		},
	}

	app.Action = func(c *cli.Context) error {

		callback := callback(make(chan error, 1))

		client, err := posthog.NewWithConfig(apiKey, posthog.Config{
			BatchSize: 1,
			Callback:  callback,
		})
		if err != nil {
			fmt.Println("could not initialize posthog client", err)
			os.Exit(1)
		}

		switch operationType {
		case "capture":
			client.Enqueue(posthog.Capture{
				DistinctId: distinctId,
				Event:      event,
				Properties: parseJSON(properties),
			})
		case "alias":
			client.Enqueue(posthog.Alias{
				DistinctId: distinctId,
				Alias:      alias,
			})
		case "identify":
			client.Enqueue(posthog.Identify{
				DistinctId: distinctId,
				Properties: parseJSON(properties),
			})
		}

		if err := <-callback; err != nil {
			os.Exit(1)
		}

		return nil
	}

	err := app.Run(os.Args)
	if err != nil {
		log.Fatal(err)
	}
}

// parseJSON parses a JSON formatted string into a map.
func parseJSON(v string) map[string]interface{} {
	var m map[string]interface{}
	err := json.Unmarshal([]byte(v), &m)
	if err != nil {
		fmt.Println("could not parse json", v)
		fmt.Println("error:", err)
		os.Exit(1)
	}
	return m
}

// callback implements the posthog.Callback interface. It is used by the CLI
// to wait for events to be uploaded before exiting.
type callback chan error

func (c callback) Failure(m posthog.APIMessage, err error) {
	fmt.Printf("could not upload message %v due to %v\n", m, err)
	c <- err
}

func (c callback) Success(_ posthog.APIMessage) {
	c <- nil
}
