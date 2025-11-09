package downloader

import (
	"path/filepath"
	"time"
)

// Creates status with start time and  and appends to statuses.
func (d *Downloader) newStatus(url, destPath string) *DownloadStatus {
	start := time.Now()
	status := &DownloadStatus{
		URL:      url,
		Filepath: destPath,
		FileName: filepath.Base(destPath),
		StartAt:  &start,
	}

	d.mu.Lock()
	d.statuses = append(d.statuses, status)
	d.mu.Unlock()
	return status
}

// Sets status endTime time. Only first call is valid.
func (status *DownloadStatus) endTime() {
	if status.EndAt != nil {
		return
	}
	end := time.Now()
	status.EndAt = &end
}

// Logs error on status as string and returns the error
func (d *Downloader) logErrorOnStatus(err error, status *DownloadStatus) error {
	status.Err = err.Error()
	d.log("error", "", status, err)
	return err
}
