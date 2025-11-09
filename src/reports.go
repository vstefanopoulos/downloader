package downloader

import (
	"encoding/json"
	"time"
)

// ExposeStatus Returns a slice with all the statuses that are running or have been completed.
func (d *Downloader) ExposeStatus() []DownloadStatus {
	statusesCopy := d.GetStatusesCopy()
	return statusesCopy
}

// ExposeStatusJSON Returns a JSON with all the statuses that are running or have completed
func (d *Downloader) ExposeStatusJSON() (string, error) {
	statusesCopy := d.GetStatusesCopy()

	fieldBytes, err := json.Marshal(statusesCopy)
	if err != nil {
		return "", err
	}
	return string(fieldBytes), nil
}

// GetStatusesCopy creates a deep copy of statuses. Used to safely expose full download status information without risking data integrity
func (d *Downloader) GetStatusesCopy() []DownloadStatus {
	d.mu.Lock()
	snapshot := make([]*DownloadStatus, len(d.statuses))
	copy(snapshot, d.statuses)
	d.mu.Unlock()

	statusesCopy := make([]DownloadStatus, len(d.statuses))
	for i, status := range snapshot {
		newStatus := *status
		if status.StartAt != nil {
			temp := *status.StartAt
			newStatus.StartAt = &temp
		}
		if status.EndAt != nil {
			temp := *status.EndAt
			newStatus.EndAt = &temp
		}
		statusesCopy[i] = newStatus
	}

	return statusesCopy
}

// GetAverageRead returns the average of read bytes from the beginning of process until call time
func (d *Downloader) GetAverageRead() int64 {
	d.mu.Lock()
	speedLogLen := int64(d.speedLog.Len())
	snapshot := []int64{}

	for e := d.speedLog.Front(); e != nil; e = e.Next() {
		val, ok := e.Value.(int64)
		if !ok {
			panic("bad value in speedLogs, should be int64 instead")
		}
		snapshot = append(snapshot, val)
	}

	d.mu.Unlock()

	if speedLogLen <= 0 {
		d.log("debug", "No Data", speedLogLen)
		return -1
	}

	var sum int64
	for _, l := range snapshot {
		sum += l
	}
	ticksPerSecond := int64(time.Second / CLOCK_INTERVAL)

	return (sum / speedLogLen) * ticksPerSecond
}

// GetAverageLastXSeconds returns the average read speed of the last n amount of seconds
func (d *Downloader) GetAverageLastXSeconds(n float64) int64 {
	sampleCount := int((time.Second * time.Duration(n)) / CLOCK_INTERVAL)
	d.mu.Lock()

	current := d.speedLog.Front()

	var total int64
	counter := 0
	for range sampleCount {
		val, ok := current.Value.(int64)
		if !ok {
			panic("bad value in speedlog instead of int64")
		}
		total += val
		counter++
		current = current.Next()

		if current == nil || val < 0 {
			break
		}
	}
	d.mu.Unlock()
	return int64(float64(total) / (n / float64(sampleCount/counter)))
}

// logSpeed logs download speed data at an interval
func (d *Downloader) logSpeed() {
	ticker, unsubscribe := d.ticker.Subscribe()
	var prevBytes int64
	go func() {
		defer unsubscribe()
		for range ticker {
			current := d.totalReadGlobal.Load()
			speed := current - prevBytes // bytes downloaded in this interval
			prevBytes = current

			d.mu.Lock()
			d.speedLog.PushFront(speed)
			d.mu.Unlock()
		}
	}()
}
