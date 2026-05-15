package aliyundrive_share2open

import "sync"

// Global alist-style token storage (aliyundrive_share2open driver needs these)
var AliOpenAccessToken string
var AliOpenRefreshToken string

// Global mutex to serialize token refresh across all instances (reduces API risk control)
var tokenMutex sync.Mutex

// Global refresh state: records who initiated the current refresh to avoid redundant calls
var refreshing bool = false