package aliyun_to_115

import "time"

// AliyunSyncCache 持久化阿里云同步 dedup cache key
type AliyunSyncCache struct {
	CacheKey string    `gorm:"primaryKey;size:128"`
	SyncedAt time.Time `gorm:"autoCreateTime"`
}

func (AliyunSyncCache) TableName() string {
	return "aliyun_sync_cache"
}