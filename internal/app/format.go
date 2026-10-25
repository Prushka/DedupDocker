package app

import "fmt"

func formatBytes(size int64) string {
	units := [...]string{"B", "KB", "MB", "GB", "TB"}
	value, unit := float64(size), 0
	for value >= 1000 && unit < len(units)-1 {
		value /= 1000
		unit++
	}
	if unit == 0 {
		return fmt.Sprintf("%d B", size)
	}
	return fmt.Sprintf("%.2f %s", value, units[unit])
}
