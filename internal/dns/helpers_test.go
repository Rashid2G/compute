package dns

import "time"

func timeNowAdd() time.Time { return time.Now().Add(2 * time.Second) }
