package main

import (
	"context"
	"log"
	
	"github.com/urfave/cli/v3"
	"github.com/urfave/sflags"
	"github.com/urfave/sflags/gen/gcli"
)

func command(cfg *config)  *cli.Command {

	cmd := &cli.Command{
			Name:        "remote-console",
			Usage:       "access remote consoles",
			Description: "OpenCHAMI remote console service",
			Before: func(ctx context.Context, c *cli.Command) (context.Context, error) {
				return ctx, validateConfig(cfg)
			},
			Action: func(context.Context, *cli.Command) error {
				return runService(*cfg)
			},
		}

	err := gcli.ParseToV3(cfg, &cmd.Flags, sflags.EnvPrefix("RCS_"))
	if err != nil {
		log.Fatalf("err: %v", err)
	}

	// Add log config separately, so we can flatten it
	err = gcli.ParseToV3(&cfg.Log, &cmd.Flags, sflags.EnvPrefix("RCS_"))
	if err != nil {
		log.Fatalf("err: %v", err)
	}

	return cmd
}