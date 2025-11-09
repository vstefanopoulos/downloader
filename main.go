package main

import (
	downloader "donwloader/src"
	"fmt"
)

func main() {
	rawConfigs := map[string]any{
		"rate-limit": int(1000 * 1000 * 2), //2MB
	}

	// Create instance
	d, warnings := downloader.NewDownloader(rawConfigs, nil)

	// Optional: log warnigs
	if len(warnings) > 0 {
		fmt.Println(warnings)
	}

	// Call download managers with URL and destination
	status, err := d.Download("https://platform.zone01.gr/git/root/public/src/branch/master/subjects/wget/audit", "./test")
	if err != nil {
		fmt.Println(err)
	}
	fmt.Println(downloader.ParseStatus(status))
}
