package main

import (
	"fmt"
	"runtime/debug"

	"github.com/spf13/cobra"
)

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print the version of workshop",
	RunE: func(cmd *cobra.Command, args []string) error {
		bi, ok := debug.ReadBuildInfo()
		if !ok {
			fmt.Println("dev")
			return nil
		}

		version := bi.Main.Version
		if version == "" || version == "(devel)" {
			version = "dev"
		}

		fmt.Println(version)
		return nil
	},
}

func init() {
	rootCmd.AddCommand(versionCmd)
}
