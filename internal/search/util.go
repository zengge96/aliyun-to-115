package search

import (
	"strings"

"github.com/OpenListTeam/OpenList/v4/internal/conf"
	"github.com/OpenListTeam/OpenList/v4/internal/driver"
	"github.com/OpenListTeam/OpenList/v4/internal/model"
	"github.com/OpenListTeam/OpenList/v4/internal/op"
	"github.com/OpenListTeam/OpenList/v4/internal/setting"
	"github.com/OpenListTeam/OpenList/v4/pkg/utils"
	log "github.com/sirupsen/logrus"
)

func Progress() (*model.IndexProgress, error) {
	p := setting.GetStr(conf.IndexProgress)
	var progress model.IndexProgress
	err := utils.Json.UnmarshalFromString(p, &progress)
	return &progress, err
}

func WriteProgress(progress *model.IndexProgress) {
	p, err := utils.Json.MarshalToString(progress)
	if err != nil {
		log.Errorf("marshal progress error: %+v", err)
	}
	err = op.SaveSettingItem(&model.SettingItem{
		Key:   conf.IndexProgress,
		Value: p,
		Type:  conf.TypeText,
		Group: model.SINGLE,
		Flag:  model.PRIVATE,
	})
	if err != nil {
		log.Errorf("save progress error: %+v", err)
	}
}

func updateIgnorePaths(customIgnorePaths string) {
	storages := op.GetAllStorages()
	ignorePaths := make([]string, 0)
	var skipDrivers = []string{"OpenList", "Virtual"}
	for _, storage := range storages {
		if utils.SliceContains(skipDrivers, storage.Config().Name) {
			if storage.Config().Name == "OpenList" {
				// openlist driver is removed in this fork, skip
				continue
			} else {
				ignorePaths = append(ignorePaths, storage.GetStorage().MountPath)
			}
		}
	}
	if customIgnorePaths != "" {
		ignorePaths = append(ignorePaths, strings.Split(customIgnorePaths, "\n")...)
	}
	conf.SlicesMap[conf.IgnorePaths] = ignorePaths
}

func isIgnorePath(path string) bool {
	for _, ignorePath := range conf.SlicesMap[conf.IgnorePaths] {
		if strings.HasPrefix(path, ignorePath) {
			return true
		}
	}
	return false
}

func init() {
	op.RegisterSettingItemHook(conf.IgnorePaths, func(item *model.SettingItem) error {
		updateIgnorePaths(item.Value)
		return nil
	})
	op.RegisterStorageHook(func(typ string, storage driver.Driver) {
		var skipDrivers = []string{"OpenList", "Virtual"}
		if utils.SliceContains(skipDrivers, storage.Config().Name) {
			updateIgnorePaths(setting.GetStr(conf.IgnorePaths))
		}
	})
}
