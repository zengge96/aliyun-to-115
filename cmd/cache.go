package cmd

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strconv"

	"github.com/OpenListTeam/OpenList/v4/cmd/flags"
	"github.com/OpenListTeam/OpenList/v4/internal/bootstrap"
	"github.com/OpenListTeam/OpenList/v4/internal/db"
	"github.com/spf13/cobra"
	_ "github.com/mattn/go-sqlite3"
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

		// 打开 work.db（与 driver 统一的数据库）
		dbPath := filepath.Join(flags.DataDir, "work.db")
		if _, err := os.Stat(dbPath); os.IsNotExist(err) {
			fmt.Printf("work.db not found at %s, nothing to clear\n", dbPath)
			return nil
		}

		workDB, err := sql.Open("sqlite3", dbPath)
		if err != nil {
			return fmt.Errorf("open work.db failed: %v", err)
		}
		defer workDB.Close()

		if len(args) == 0 {
			// 不带参数：清所有 aliyun_sync_cache
			result, err := workDB.Exec("DELETE FROM aliyun_sync_cache")
			if err != nil {
				return fmt.Errorf("delete failed: %v", err)
			}
			n, _ := result.RowsAffected()
			fmt.Printf("Cleared %d cache entries from aliyun_sync_cache\n", n)
			return nil
		}

		// 带参数：按 storage_id 找到 mount_path，按 mount_path 前缀删
		id, err := strconv.Atoi(args[0])
		if err != nil {
			return fmt.Errorf("id must be a number")
		}

		storage, err := db.GetStorageById(uint(id))
		if err != nil {
			return fmt.Errorf("failed to get storage: %v", err)
		}

		if storage.Driver != "aliyun_to_115" {
			return fmt.Errorf("only aliyun_to_115 storage is supported, got: %s", storage.Driver)
		}

		prefix := storage.MountPath + "/%"
		result, err := workDB.Exec("DELETE FROM aliyun_sync_cache WHERE cache_key LIKE ?", prefix)
		if err != nil {
			return fmt.Errorf("delete failed: %v", err)
		}
		n, _ := result.RowsAffected()
		fmt.Printf("Cleared %d cache entries for storage [%s] (mount_path=%s)\n", n, storage.MountPath, storage.MountPath)
		return nil
	},
}

func init() {
	RootCmd.AddCommand(cacheCmd)
	cacheCmd.AddCommand(clearCacheCmd)
}