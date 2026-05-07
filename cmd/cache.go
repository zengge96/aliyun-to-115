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
	Short: "Clear sync cache for aliyun_to_115 storage(s)",
	RunE: func(cmd *cobra.Command, args []string) error {
		bootstrap.Init()
		defer bootstrap.Release()

		if len(args) == 0 {
			// 不带参数：清所有
			result := db.GetDb().Exec("DELETE FROM aliyun_sync_cache")
			fmt.Printf("Cleared %d cache entries from aliyun_sync_cache table\n", result.RowsAffected)
			return nil
		}

		// 带参数：校验 storage 类型后清所有（cache_key 不区分存储，按 mountPath 过滤暂不可用）
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("id must be a number")
		}

		storage, err := db.GetStorageById(uint(id))
		if err != nil {
			return fmt.Errorf("failed to get storage: %+v", err)
		}

		if storage.Driver != "aliyun_to_115" {
			return fmt.Errorf("only aliyun_to_115 storage is supported, got: %s", storage.Driver)
		}

		result := db.GetDb().Exec("DELETE FROM aliyun_sync_cache")
		fmt.Printf("Cleared %d cache entries (storage_id=%d, mount_path=%s)\n", result.RowsAffected, id, storage.MountPath)
		return nil
	},
}

func init() {
	RootCmd.AddCommand(cacheCmd)
	cacheCmd.AddCommand(clearCacheCmd)
}