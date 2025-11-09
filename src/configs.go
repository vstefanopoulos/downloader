package downloader

import (
	"fmt"
	"net/http"
	"reflect"
	"time"

	"golang.org/x/time/rate"
)

type configs struct {
	RateLimit      int
	MaxDownloaders int
	NoOverwrite    bool
	Resume         bool
	Timestamping   bool
	RetryCount     int
}

func NewDownloader(rawConfigs map[string]any, log loggerFunc) (*Downloader, []string) {

	configs, warnings := loadConfigs(rawConfigs)

	rateLimit := func() rate.Limit {
		if configs.RateLimit == 0 {
			return rate.Inf
		} else {
			return rate.Limit(configs.RateLimit)
		}
	}()

	client := &http.Client{
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
			DisableCompression:  true,
		},
		Timeout: 0,
	}

	ticker := NewTickerFanOut(CLOCK_INTERVAL)

	logger := func() loggerFunc {
		if log == nil {
			return customLogger
		}
		return log
	}()

	return &Downloader{
		StartTime:     time.Now(),
		log:           logger,
		networkStats:  assignNetStatFunc(),
		rateLimiter:   rate.NewLimiter(rateLimit, int(rateLimit)/(configs.MaxDownloaders)),
		client:        client,
		ticker:        ticker,
		stm:           hostsRateLimit{hosts: make(map[string]time.Time)},
		downloadSlots: make(chan struct{}, configs.MaxDownloaders),
		destPaths:     make(map[string]struct{}),
		extConfigs:    configs,
		resume:        configs.Resume,
	}, warnings
}

func loadConfigs(rawConfigs map[string]any) (configs, []string) {
	warnings := []string{}
	var defaultConf = configs{
		RateLimit:      0,
		MaxDownloaders: 20,
		NoOverwrite:    false,
		Resume:         false,
		Timestamping:   false,
		RetryCount:     3,
	}

	if rawConfigs == nil {
		return defaultConf, []string{"no configs provided running with defaults"}
	}

	assignIfOk(rawConfigs, "rate-limit", &defaultConf.RateLimit, &warnings)
	assignIfOk(rawConfigs, "no-overwrite", &defaultConf.NoOverwrite, &warnings)
	assignIfOk(rawConfigs, "max-downloaders", &defaultConf.MaxDownloaders, &warnings)
	assignIfOk(rawConfigs, "continue", &defaultConf.Resume, &warnings)
	assignIfOk(rawConfigs, "timestamping", &defaultConf.Timestamping, &warnings)
	assignIfOk(rawConfigs, "retry-count", &defaultConf.RetryCount, &warnings)
	return defaultConf, warnings
}

// Assigns value to destination, if value is found in map and value type matches destination type, else it logs a warning
func assignIfOk[T any](m map[string]any, key string, target *T, warnings *[]string) error {
	if v, ok := m[key]; ok {
		if casted, ok := v.(T); ok {
			*target = casted
			return nil
		}
		err := fmt.Errorf("bad config.ini value: key: %s, val: %v <- problem. expected type of: %v, and received %v", key, m[key], reflect.TypeOf(*target), reflect.TypeOf(v))
		*warnings = append(*warnings, err.Error())
		return err

	} else {
		return nil
	}
}
