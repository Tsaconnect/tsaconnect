package services

import "time"

// Tests do not wait a second for each lane's first-use floor re-read.
func init() { keyLaneBootReread = 10 * time.Millisecond }
