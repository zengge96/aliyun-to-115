package cmd

import (
	"fmt"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/spf13/cobra"
)

var cacheCmd = &cobra.Command{
	Use:   "cache",
	Short: "Manage cache",
}

var clearCacheCmd = &cobra.Command{
	Use:   "clear [storage_id]",
	Short: "Clear sync cache for an aliyun_to_115 storage",
	RunE: func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("storage_id is required")
		}
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("id must be a number")
		}

		bootstrap.Init()
		defer bootstrap.Release()

		storage, err := db.GetStorageById(uint(id))
		if err != nil {
			return fmt.Errorf("failed to get storage: %+v", err)
		}

		if storage.Driver != "aliyun_to_115" {
			return fmt.Errorf("only aliyun_to_115 storage is supported, got: %s", storage.Driver)
		}

		result := db.GetDb().Exec("DELETE FROM aliyun_sync_cache WHERE cache_key LIKE ?", storage.MountPath+"/%")
		fmt.Printf("Cleared %d cache entries for storage [%s] (%s)\n", result.RowsAffected, storage.MountPath, storage.Driver)
		return nil
	},
}

func init() {
	RootCmd.AddCommand(cacheCmd)
	cacheCmd.AddCommand(clearCacheCmd)
}