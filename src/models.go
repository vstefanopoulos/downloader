package downloader

import (
	"container/list"
	"io"
	"net/http"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/time/rate"
)

const (
	// The persumed overhead precentage per request
	PROTOCOL_OVERHEAD_RATIO float64       = 0.02
	REQUESTS_PER_SECOND     int           = 15
	MIN_BUFFER              int           = 1024
	MAX_BUFFER              int           = 4 * 1024 * 1024
	STAMPEDE_SLEEP_AMOUNT   time.Duration = time.Duration(150) * time.Millisecond
	CLOCK_INTERVAL          time.Duration = time.Duration(50) * time.Millisecond
)

// This is a JSON tagged struct representing the current status of the downloader
// Status is unique to each donwload. Its not ment for concurrent writes or reads
type DownloadStatus struct {
	FileName            string     `json:"file_name"`
	StartAt             *time.Time `json:"start_at"`
	EndAt               *time.Time `json:"end_at"`
	StatusRes           string     `json:"status_res"`
	URL                 string     `json:"url"`
	Filepath            string     `json:"filepath"`
	TotalEstimatedBytes int64      `json:"total_bytes"`
	ReadBytes           int64      `json:"downloaded_bytes"`
	Err                 string     `json:"error"`
}

// The global downloader instances shared between routines
// Holds configs, tickers, writers logging and reporting objects
type Downloader struct {
	// Logging and status
	StartTime             time.Time         // Global start time. Updated on very start
	totalReadGlobal       atomic.Int64      // Total downloaded bytes
	statuses              []*DownloadStatus // slice that holds status updates from all downloads
	FileLogger            loggerFunc        // specilized logger to write final status on file
	log                   loggerFunc        // function to use for logging on terminal and/or file
	runningDownloadsCount atomic.Int32      // counter of downloads currently in progress
	initialized           atomic.Bool

	// Speed
	speedLog     list.List           // all download chunks every tick interval
	networkStats func(d *Downloader) // function that gets the net activity from OS for current downloader

	// Global utils and checks
	rateLimiter *rate.Limiter // limiting requests or reads
	client      *http.Client
	ticker      *TickerFanOut // Global ticker fan outted``
	stm         hostsRateLimit

	// Concurency
	mu            sync.Mutex
	downloadSlots chan struct{}       // channel that holds the queue of downloads
	destPaths     map[string]struct{} // holds all active writes in paths

	// Configurations
	extConfigs configs
	resume     bool // flag to resume previous donwload
}

type hostsRateLimit struct {
	mu    sync.Mutex
	hosts map[string]time.Time // holds the host name and the time when next request is allowed
}

// RateLimitedReader wraps an io.Reader and uses a shared RateLimiter.
type RateLimitedReader struct {
	r          io.Reader
	limiter    *rate.Limiter
	readBuffer int // dynamic varied readBuffer per donwload reader
}

type loggerFunc func(level string, msg string, extras ...any) error
