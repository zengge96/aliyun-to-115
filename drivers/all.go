package drivers

import (
	_ "github.com/OpenListTeam/OpenList/v4/drivers/115"
	_ "github.com/OpenListTeam/OpenList/v4/drivers/115_share"
	_ "github.com/OpenListTeam/OpenList/v4/drivers/alias"
	_ "github.com/OpenListTeam/OpenList/v4/drivers/aliyun_to_115"
	_ "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_open"
	_ "github.com/OpenListTeam/OpenList/v4/drivers/aliyundrive_share2open"
)

// All do nothing,just for import
// same as _ import
func All() {
}