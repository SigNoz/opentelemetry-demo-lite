package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"otel-mock/scenario"
	"time"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
)

func main() {
	if err := run(os.Args[1:], os.Stdout, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("scenario", flag.ContinueOnError)
	flags.SetOutput(stderr)
	world := flags.String("world", "", "clean-checkout-v1 or scoped-cpu-comparison-v1 (required)")
	runID := flags.String("run-id", "", "private controller run ID (required; never exported)")
	reference := flags.String("reference-time", "", "shared RFC3339 reference time, at a whole second (required)")
	format := flags.String("format", "manifest", "stdout format: manifest, otlp-json or otlp-proto")
	execute := flags.Bool("execute", false, "execute the finite plan; also requires EVAL_MODE=1")
	endpoint := flags.String("endpoint", "", "explicit local OTLP/HTTP /v1/metrics URL, required for CPU execution")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 {
		return fmt.Errorf("unexpected positional arguments")
	}
	clock, err := time.Parse(time.RFC3339Nano, *reference)
	if err != nil {
		return fmt.Errorf("reference-time is required and must be RFC3339: %w", err)
	}
	fixture, err := scenario.Build(*world, *runID, clock)
	if err != nil {
		return err
	}
	if *endpoint != "" {
		if err := scenario.ValidateEndpoint(*endpoint); err != nil {
			return err
		}
	}
	var data []byte
	switch *format {
	case "manifest":
		data, err = json.MarshalIndent(fixture.Manifest, "", "  ")
	case "otlp-json":
		data, err = (protojson.MarshalOptions{Indent: "  "}).Marshal(fixture.Metrics)
	case "otlp-proto":
		data, err = proto.Marshal(fixture.Metrics)
	default:
		return fmt.Errorf("format must be manifest, otlp-json or otlp-proto")
	}
	if err != nil {
		return err
	}
	if *execute {
		if err := scenario.Execute(context.Background(), fixture, *endpoint); err != nil {
			return err
		}
	}
	if *format != "otlp-proto" {
		data = append(data, '\n')
	}
	_, err = stdout.Write(data)
	return err
}
