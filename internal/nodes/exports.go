package nodes

// Exported helpers for health and validation

func GetHardwareUpdatePeriodSec() int {
	return newHardwareCheckPeriodSec
}

func GetHardwareUpdateTime() string {
	return hardwareUpdateTime
}

func GetNodeCacheLen() int {
	return len(nodeCache)
}

func NodeCacheHas(id string) bool {
	_, ok := nodeCache[id]
	return ok
}
