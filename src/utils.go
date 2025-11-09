package downloader

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func customLogger(level, msg string, extras ...any) error {
	fieldBytes, err := json.Marshal(extras)
	if err != nil {
		fieldBytes = []byte{}
	}
	extrasStr := string(fieldBytes)

	// Open (or create) log file for appending
	f, err := os.OpenFile("downloader-log", os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
	if err != nil {
		return fmt.Errorf("failed to open log file: %w", err)
	}
	defer f.Close()

	// Format log message
	logLine := fmt.Sprintf("%s: %s | %s\n", strings.ToUpper(level), msg, extrasStr)

	// Write log to file
	if _, err := f.WriteString(logLine); err != nil {
		return fmt.Errorf("failed to write log: %w", err)
	}

	return nil
}

// Checks if given path is in active downloads paths map and if not it adds it. Returns true if path is taken by another process.
func (d *Downloader) pathInUse(destPath string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	if _, exists := d.destPaths[destPath]; exists {
		return true
	}
	d.destPaths[destPath] = struct{}{}
	return false
}

// Removes path from active downloads paths map upon completion of download.
func (d *Downloader) releasePath(destPath string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	delete(d.destPaths, destPath)
}

// Locks for as long as running downloads are above max downloaders limit. If a slot is available it increments running downloads count.
func (d *Downloader) acquireSlot() {
	d.downloadSlots <- struct{}{}
	d.runningDownloadsCount.Add(1)
}

// Draws one slot from d.downloadSlots and decrements running downloads count.
func (d *Downloader) releaseSlot() {
	<-d.downloadSlots
	d.runningDownloadsCount.Add(-1)
}

// Rate limits consecutive requests to same host. The time interval between requests is set on const STAMPEDE_SLEEP_AMOUNT.
func (d *Downloader) allowCallOnHost(s *DownloadStatus) {
	d.stm.mu.Lock()
	defer d.stm.mu.Unlock()

	u, _ := url.Parse(s.URL)
	host := u.Host

	allowN, found := d.stm.hosts[host]
	if !found {
		d.stm.hosts[host] = time.Now().Add(STAMPEDE_SLEEP_AMOUNT)
		return
	}

	diff := time.Since(allowN)
	if diff < 0 {
		time.Sleep(-diff)
	}
	d.stm.hosts[host] = time.Now().Add(STAMPEDE_SLEEP_AMOUNT)
}

// Checks if url and destination path are present and valid.
func (d *Downloader) preflightChecks(status *DownloadStatus) (err error) {
	if status.URL == "" {
		return fmt.Errorf("%w: no url given", ErrPreFlightTests)
	}

	if status.Filepath == "" {
		return fmt.Errorf("%w: no destination path given", ErrPreFlightTests)
	}
	return nil
}

// Advice user to change configs if status is 429.
// Probable cause is either too many workers are requesting headers or rate limit is too low resulting in to many small requests
func (d *Downloader) adviseUser429(status *DownloadStatus) {
	if d.extConfigs.MaxDownloaders > 1 || d.extConfigs.RateLimit != 0 {
		d.log(
			"info",
			"Got too many requests. Try again with less downloaders and/or higher rate limit",
			status,
			map[string]any{
				"maxDownloaders":      d.extConfigs.MaxDownloaders,
				"running downloaders": d.runningDownloadsCount.Load(),
				"rate-limit":          d.extConfigs.RateLimit,
			})
	}
}

// Checks destination os.Stat and returns the last-modified time if the file exists,
// or the zero time if it does not.
// If d.Filepath doesn't exist it attempts to create dir. If any other error occurs it is wraped and returned.
func (d *Downloader) checkDestinationFile(status *DownloadStatus) (lastModified time.Time, err error) {
	stat, err := os.Stat(status.Filepath)
	if err == nil {
		lastModified = stat.ModTime()

		if d.resume || d.extConfigs.Timestamping {
			return lastModified, nil
		}

		if d.extConfigs.NoOverwrite {
			return lastModified, fmt.Errorf("%w: destination file already exists: %s", ErrPreFlightTests, status.Filepath)
		}

		// If overwrite is allowed, remove the existing file first (important for Windows)
		if err := os.Remove(status.Filepath); err != nil {
			return lastModified, errors.Join(ErrPreFlightTests, fmt.Errorf("%w: could not remove existing file: %s", err, status.FileName))
		}

	} else if os.IsNotExist(err) {
		// Create dir
		if err := os.MkdirAll(filepath.Dir(status.Filepath), 0755); err != nil {
			return lastModified, errors.Join(ErrPreFlightTests, fmt.Errorf("%w: could not create destination directory: %s", err, status.Filepath))
		}

	} else {
		return lastModified, errors.Join(ErrPreFlightTests, fmt.Errorf("%w: could not check destination file: %s", err, status.Filepath))
	}
	return lastModified, nil
}

// Attempts to open file in append mode if flag resume or read/write mode.
func (d *Downloader) openFile(partialPath string) (out *os.File, err error) {
	if d.resume {
		out, err = os.OpenFile(partialPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			return out, fmt.Errorf("%w: error opening file in append mode %w", ErrOpenFile, err)
		}
	} else {
		out, err = os.OpenFile(partialPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0666)
		if err != nil {
			return out, fmt.Errorf("%w: could not create partial file: %w", ErrOpenFile, err)
		}
	}
	return out, nil
}

// Checks lastModified for timeIsZero value to asses if file prexisted. If not it compares the modification time with the header "Last-Modified" field.
func (d *Downloader) isFileUpToDate(head *http.Response, lastModified time.Time) bool {
	if lastModified.IsZero() {
		return false
	}
	if headerLastMod, err := getLastModified(head); err == nil &&
		!headerLastMod.After(lastModified) {
		return true
	}
	return false
}

// Attempts to get the response 'Last-Modified' value and parses it to time.Time format.
// Returns error if 'Last-Modified' is not found or ParseTime() fails.
func getLastModified(resp *http.Response) (lastMod time.Time, err error) {
	lastModHeader := resp.Header.Get("Last-Modified")
	if lastModHeader != "" {
		lastMod, err = http.ParseTime(lastModHeader)
		if err == nil {
			return lastMod, nil
		} else {
			return lastMod, errors.Join(ErrGetLastModified, fmt.Errorf("%w: last-modified header parsing error", err))
		}
	} else {
		return lastMod, fmt.Errorf("%w: no last-modified header found", ErrGetLastModified)
	}
}

// Closes file and renames it to the original destination path.
func (d *Downloader) closeAndRename(out *os.File, partialPath string, status *DownloadStatus) error {
	err := out.Close()
	if err != nil {
		return errors.Join(ErrCloseAndRename, fmt.Errorf("%w: could not close temporary file", err))
	}

	err = os.Rename(partialPath, status.Filepath)
	if err != nil {
		return errors.Join(ErrCloseAndRename, fmt.Errorf("%w: could not rename partial file", err))
	}
	return nil
}

// bytesToHuman converts a byte count to a human-readable string using binary units.
func (d *Downloader) bytesToHuman(b int64) string {
	const unit = 1024
	if b < unit {
		return fmt.Sprintf("%d B", b)
	}
	div, exp := uint64(unit), 0
	for n := b / unit; n >= unit; n /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.2f %ciB", float64(b)/float64(div), "KMGTPE"[exp])
}

// Converts 's' to string and logs through the designated logger for logging to file if -B (background) flag is true.
// The way to check if the flag is true is by checking if 'd.FileLogger' field is not nil.
// The 'FileLogger' func could be any logger that satisfies the 'loggerFunc' signature and its usage covers the case of exposing a channel that streams the logs to the caller.
func (d *Downloader) LogToFile(s *DownloadStatus) {
	if d.FileLogger == nil {
		return
	}
	finalStatus := ParseStatus(s)
	d.FileLogger("info", finalStatus)
}

// Converts bytes to readable string in decimal units.
func bytesToReadableSize(b int64) string {
	const (
		KB = 1000
		MB = 1000 * KB
		GB = 1000 * MB
	)

	switch {
	case b >= GB:
		return fmt.Sprintf("%.2fGB", float64(b)/float64(GB))
	case b >= MB:
		return fmt.Sprintf("%.2fMB", float64(b)/float64(MB))
	case b >= KB:
		return fmt.Sprintf("%.2fKB", float64(b)/float64(KB))
	default:
		return fmt.Sprintf("%dB", b)
	}
}

// Parses 's' struct to redable string.
func ParseStatus(s *DownloadStatus) string {
	return fmt.Sprintf("start at %s\nsending request, awaiting response... status %s\ncontent size: %d [~%s]\nsaving file to: %s\nDownloaded [%s]\nfinished at %s\n\n",
		s.StartAt.Local().Format(time.DateTime),
		s.StatusRes,
		s.ReadBytes,
		bytesToReadableSize(s.ReadBytes),
		s.Filepath,
		s.URL,
		s.EndAt.Local().Format(time.DateTime),
	)
}
