package downloader

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"time"

	"golang.org/x/time/rate"
)

// Download donwloads a file from url to destPath. Can be called concurrently with different destination paths.
func (d *Downloader) Download(url, destPath string) (status *DownloadStatus, err error) {
	d.log("info", fmt.Sprintf("Download called. url:%s, destPath:%s", url, destPath))

	// Add process in to do queue
	d.acquireSlot()
	defer d.releaseSlot()

	status = d.newStatus(url, destPath)

	for range max(1, d.extConfigs.RetryCount) {
		err = d.runDownload(status)
		if err == nil {
			break
		}
		time.Sleep(500 * time.Millisecond)
		d.log("warn", "Download failed. Retrying", map[string]any{"error": err.Error(), "status": status})
	}

	if err != nil {
		return status, d.logErrorOnStatus(err, status)
	}

	status.endTime()

	d.log("info", "Download Complete", map[string]any{
		"url":                url,
		"Http Response":      status.StatusRes,
		"Destination":        destPath,
		"Total Downloaded":   status.ReadBytes,
		"Average Read Speed": d.GetAverageRead(),
		"Start Time":         status.StartAt.Local().Format(time.DateTime),
		"End Time":           status.EndAt.Local().Format(time.DateTime),
	})

	d.LogToFile(status)
	return status, nil
}

// Runs preflight checks and handles file creating, opening and closing.
// Aquires download worker when available
// It creates partial path and renames it to status.Filepath when download is complete.
//
// If rate limit is not Inf and server accepts ranges it calls ranged download else it downloads with one GET.
func (d *Downloader) runDownload(status *DownloadStatus) error {
	err := d.preflightChecks(status)
	if err != nil {
		return errors.Join(ErrRunDownload, err)
	}

	// Check if path is already in use.
	if d.pathInUse(status.Filepath) {
		return fmt.Errorf("%w: already existing process in writing to destination path %s", ErrRunDownload, status.Filepath)
	}
	defer d.releasePath(status.Filepath)

	// Get file info and check if should replace.
	lastModified, err := d.checkDestinationFile(status)
	if err != nil {
		return errors.Join(ErrRunDownload, err)
	}

	d.allowCallOnHost(status)

	head, err := d.getHeader(status)
	if err != nil {
		return errors.Join(ErrRunDownload, err)
	}

	if d.extConfigs.Timestamping && d.isFileUpToDate(head, lastModified) {
		d.log("info", "File is already up to date with server", status.FileName)
		return nil
	}

	// Spawn network and read report functions on first call
	if !d.initialized.Swap(true) {
		d.startNetReporting()
		d.logSpeed()
	}

	partialPath := status.Filepath + ".part"

	var out *os.File
	out, err = d.openFile(partialPath)
	if err != nil {
		return errors.Join(ErrRunDownload, err)
	}
	defer out.Close()

	// Parse response header and download with ranged or single get download
	if head.ContentLength > 0 && head.Header.Get("Accept-Ranges") == "bytes" {
		err = d.rangeDownload(out, head.ContentLength, status)
	} else {
		if d.resume {
			d.log("warn", fmt.Sprintf("ranged download not allowed. Cannot resume url: %s. Overwritting file %s", status.URL, status.Filepath))
		}
		err = d.getDownloader(out, status)
	}

	if err != nil {
		return errors.Join(ErrRunDownload, err)
	}

	if err := d.closeAndRename(out, partialPath, status); err != nil {
		return errors.Join(ErrRunDownload, err)
	}

	return nil
}

// Updates status with head request results
func (d *Downloader) getHeader(status *DownloadStatus) (head *http.Response, err error) {
	_, err = url.ParseRequestURI(status.URL)
	if err != nil {
		return nil, errors.Join(ErrGetHeader, fmt.Errorf("invalid URL: %v %w", status.URL, err))
	}

	head, err = http.Head(status.URL)
	if err != nil {
		if head != nil {
			defer head.Body.Close()
			status.StatusRes = head.Status
		}
		return head, err
	}
	head.Body.Close()

	if head.StatusCode != http.StatusOK {
		status.StatusRes = head.Status
		if head.StatusCode == http.StatusTooManyRequests {
			d.adviseUser429(status)
		}
		return head, fmt.Errorf("%w: server returned: %v", ErrGetHeader, head.Status)
	}

	status.StatusRes = head.Status
	status.TotalEstimatedBytes = head.ContentLength
	return head, nil
}

// Downloads with header including range and applies rate limit before each range request download for finer network control.
// Each ranged request size is dynamically adjusted based on running downloaders, requests per second optimization and the protocol's overhead ratio.
func (d *Downloader) rangeDownload(out *os.File, size int64, status *DownloadStatus) error {
	d.log("info", "Downloading in chunks", map[string]any{"rate limit": d.rateLimiter.Limit()})
	stat, err := out.Stat()
	if err != nil {
		return errors.Join(ErrRangeDownload, err)
	}

	start := stat.Size()
	if start != 0 {
		d.log("debug", "Resuming download", map[string]any{"File": status.FileName, "Start": d.bytesToHuman(start)})
	}

	status.ReadBytes += int64(start)
	for {
		if start >= size {
			break
		}

		reqRange, reqLimit := d.setBurstAndReqLimit()

		end := start + int64(reqRange) - 1
		if end >= size {
			end = size - 1
		}

		err := d.rateLimiter.WaitN(context.Background(), min(reqLimit, int(end-start+1)))
		if err != nil {
			return errors.Join(ErrRangeDownload, err)
		}

		req, err := http.NewRequest("GET", status.URL, nil)
		if err != nil {
			return errors.Join(ErrRangeDownload, err)
		}

		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", start, end))

		d.log("debug", "asking for bytes", d.bytesToHuman(end-start))

		resp, err := d.client.Do(req)
		if err != nil {
			return errors.Join(ErrRangeDownload, err)
		}

		if resp.StatusCode != http.StatusPartialContent && resp.StatusCode != http.StatusOK {
			status.Err = fmt.Sprintf("Server responded with %s", resp.Status)
			d.adviseUser429(status)
			resp.Body.Close()
			return errors.Join(ErrRangeDownload, fmt.Errorf("%w: %s", ErrUnexpectedStatus, resp.Status))
		}

		n, err := d.copy(out, resp.Body, status)
		if err != nil {
			return errors.Join(ErrRangeDownload, err)
		}

		err = resp.Body.Close()
		if err != nil {
			return errors.Join(ErrRangeDownload, err)
		}

		start += n
	}

	return nil
}

// Plain get download. Rate limit is applied on reads.
func (d *Downloader) getDownloader(out *os.File, status *DownloadStatus) error {
	d.log("info", "Unknown body size. Downloading with one GET")

	resp, err := d.client.Get(status.URL)
	if err != nil {
		return errors.Join(ErrGetDownload, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return errors.Join(ErrGetDownload, fmt.Errorf("%w: %s", ErrUnexpectedStatus, resp.Status))
	}

	_, readBuffer := d.setBurstAndReqLimit()

	reader := NewRateLimitedReader(resp.Body, d.rateLimiter, readBuffer)

	if _, err = d.copy(out, reader, status); err != nil {
		return errors.Join(ErrGetDownload, err)
	}
	return nil
}

// Dynamically calculates request limit according to running downloads and set requests per second.
// Resets the limiter burst so the tokens asked from WaitN to never excide the burst limit.
// Returns request range (usefull for ranged requests) and request limit (limiter tokens for current request).
func (d *Downloader) setBurstAndReqLimit() (reqRange int, reqLimit int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	// no rate limit
	if d.rateLimiter.Limit() == rate.Inf {
		return MAX_BUFFER, MAX_BUFFER
	}

	// Set burst limit
	reqLimit = (int(d.rateLimiter.Limit()) / int(d.runningDownloadsCount.Load())) / REQUESTS_PER_SECOND
	reqLimit = max(MIN_BUFFER, reqLimit)
	d.rateLimiter.SetBurst(int(reqLimit))

	// Reducing request range to compensate for request overhead
	reqRange = int(float64(reqLimit) * (1 - PROTOCOL_OVERHEAD_RATIO))

	return reqRange, reqLimit
}

// Custom implementation of io copyBuffer.
// It updates d.totalReadGlobal and status.ReadBytes on every read.
// When on single get request the io.Reader passed here is rate limited.
func (d *Downloader) copy(dst io.Writer, src io.Reader, status *DownloadStatus) (written int64, err error) {
	for {
		bufSize := min(max(d.rateLimiter.Burst(), MIN_BUFFER), MAX_BUFFER)
		buf := make([]byte, bufSize)

		nr, er := src.Read(buf)

		if nr > 0 {

			// Update status and global read bytes
			status.ReadBytes += int64(nr)
			d.totalReadGlobal.Add(int64(nr))

			nw, ew := dst.Write(buf[0:nr])
			if nw < 0 || nr < nw {
				nw = 0
				if ew == nil {
					ew = errors.Join(ErrCopy, fmt.Errorf("copy error: invalid write result"))
				}
			}
			written += int64(nw)
			if ew != nil {
				err = ew
				break
			}
			if nr != nw {
				err = errors.Join(ErrCopy, fmt.Errorf("copy error: short write"))
				break
			}
		}
		if er != nil {
			if !errors.Is(er, io.EOF) {
				err = er
			}
			break
		}
	}
	return written, err
}

// Provides a new reader with global rate limit. Optimized for concurrent use from all downloads.
func NewRateLimitedReader(r io.Reader, limiter *rate.Limiter, readBuf int) *RateLimitedReader {
	return &RateLimitedReader{r: r, limiter: limiter, readBuffer: readBuf}
}

// Rate limited reader that is passed to copy when limiting reads instead of download range.
func (rlr *RateLimitedReader) Read(p []byte) (int, error) {
	lenP := len(p)

	if lenP > rlr.readBuffer {
		p = p[:rlr.readBuffer]
	}

	err := rlr.limiter.WaitN(context.Background(), lenP)
	if err != nil {
		return 0, errors.Join(ErrLimitedRead, err)
	}
	n, err := rlr.r.Read(p)
	if err != nil {
		return n, errors.Join(ErrLimitedRead, err)
	}

	return n, nil
}
