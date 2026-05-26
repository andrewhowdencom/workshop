package main

import (
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

var threadCmd = &cobra.Command{
	Use:   "thread",
	Short: "Manage persistent threads",
}

var threadListCmd = &cobra.Command{
	Use:   "list",
	Short: "List persistent threads",
	RunE: func(cmd *cobra.Command, args []string) error {
		return nil
	},
}

func init() {
	threadListCmd.Flags().Int("days", 30, "Lookback period in days")
	cobra.CheckErr(viper.BindPFlags(threadListCmd.Flags()))

	threadCmd.AddCommand(threadListCmd)
	rootCmd.AddCommand(threadCmd)
}
