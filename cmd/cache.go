package cmd

import (
	"fmt"

	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/spf13/cobra"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage cache",
}

var clearCacheCmd = &cobra.Command{
	Use:   "clear",
	Short: "Clear all aliyun_to_115 sync cache records",
	RunE: func(cmd *cobra.Command, args []string) error {
		bootstrap.Init()
		defer bootstrap.Release()

		result := db.GetDb().Exec("DELETE FROM aliyun_sync_cache")
		fmt.Printf("Cleared %d cache entries from aliyun_sync_cache table\n", result.RowsAffected)
		return nil
	},
}

func init() {
	RootCmd.AddCommand(cacheCmd)
	cacheCmd.AddCommand(clearCacheCmd)
}