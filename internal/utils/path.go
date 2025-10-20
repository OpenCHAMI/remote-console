package utils

import (
	"log"
	"os"
)

func EnsureDirPresent(dir string, perm os.FileMode) (bool, error) {
	if _, err := os.Stat(dir); os.IsNotExist(err) {
		log.Printf("Directory does not exist, creating: %s", dir)
		err = os.MkdirAll(dir, perm)
		if err != nil {
			log.Printf("Unable to create dir: %s", err)
			return false, err
		}
	}
	return true, nil
}