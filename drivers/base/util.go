package base

import "time"

// V115 download rate limiting (non-VIP)
var V115novip = 1
var V115count = 0
var V115lasttime = time.Now()
var V115countwindow = 24 * time.Hour